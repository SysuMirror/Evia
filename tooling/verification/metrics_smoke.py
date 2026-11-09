#!/usr/bin/env python3
"""Verify independent metrics using real local HTTP, ingestion and query workers.

Use browser_runtime.py run PRIVATE_ENV -- python metrics_smoke.py --binary BINARY,
or pass --runtime-env PRIVATE_ENV. Only synthetic accounts and new fixtures are
created. No existing fixtures, dependency processes or data are removed.
"""
import argparse
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys
import tempfile
import time
import urllib.parse
import uuid

from backend_smoke import HTTP, SmokeFailure, definition, identifier, require, resource_id, timestamp
from browser_runtime import NAMESPACE, load

EVENT_KEY = 'metrics_smoke_page'
PERMISSIONS = ['metrics.read', 'metrics.write', 'metrics.publish', 'metrics.run', 'queries.read', 'queries.run']


class MetricsSmoke:
    def __init__(self, api, origin, binary, timeout):
        target = urllib.parse.urlsplit(api)
        require(target.scheme == 'http' and target.hostname in ('127.0.0.1', 'localhost', '::1')
                and not target.username and not target.password and target.path in ('', '/')
                and not target.query and not target.fragment, 'Metrics verification requires a loopback HTTP runtime')
        require(Path(binary).is_file(), 'A built backend binary is required')
        require(bool(os.environ.get('EVIA_DATABASE_URL')) and NAMESPACE.fullmatch(os.environ.get('EVIA_CLICKHOUSE_DATABASE', '')),
                'A dedicated synthetic browser runtime environment is required')
        self.api, self.origin, self.binary, self.timeout = api, origin, binary, timeout
        self.owner, self.public = HTTP(api, origin, session=True), HTTP(api)
        self.calls, self.query_jobs = 0, 0
        self.deadline = time.monotonic() + 300
        self.started = datetime.now(timezone.utc) - timedelta(seconds=5)
        self.project, self.source = None, None
        self.machines = []
        self.pending_queries = set()
        self.logged_in = False

    @property
    def base(self):
        return '/api/v1/projects/' + self.project

    def request(self, client, method, path, body=None, headers=None, expected=(200,)):
        self.calls += 1
        require(self.calls <= 1000 and time.monotonic() < self.deadline, 'Metrics verification exhausted its request/time budget')
        value, response_headers, status = client.request(method, path, body, headers, expected)
        if path.startswith('/api/'):
            require('no-store' in response_headers.get('Cache-Control', ''), 'Metrics control response lacks no-store')
        return value, response_headers, status

    def owner_request(self, method, path, body=None, headers=None, expected=(200,)):
        return self.request(self.owner, method, path, body, headers, expected)[0]

    def wait(self, read, ready, label):
        deadline = min(self.deadline, time.monotonic() + self.timeout)
        while True:
            value = read()
            if ready(value):
                return value
            require(time.monotonic() < deadline, label + ' did not become ready before the deadline')
            time.sleep(0.25)

    def provision(self):
        username, password = 'metrics-' + uuid.uuid4().hex[:20], secrets.token_urlsafe(24)
        environment = {**os.environ, 'EVIA_ACCOUNT_USERNAME': username, 'EVIA_ACCOUNT_PASSWORD': password,
                       'EVIA_ACCOUNT_DISPLAY_NAME': 'Synthetic metrics owner'}
        try:
            result = subprocess.run([self.binary, 'account-create'], env=environment, capture_output=True, timeout=30, check=False)
        except (OSError, subprocess.TimeoutExpired):
            raise SmokeFailure('Metrics account provisioning could not complete') from None
        require(result.returncode == 0, 'Metrics account provisioning failed; inspect private operator diagnostics')
        # Subprocess output can contain private operator data; never forward it.
        session = self.owner_request('POST', '/api/v1/session/login', {'username': username, 'password': password})
        self.owner.csrf, self.logged_in = session['csrf_token'], True
        project = self.owner_request('POST', '/api/v1/projects', {'name': 'Synthetic metrics ' + uuid.uuid4().hex[:8], 'timezone': 'UTC'}, expected=(201,))
        self.project = resource_id(project['id'])

    def publish_event(self):
        events = self.base + '/event-definitions'
        self.owner_request('POST', events, {'event_key': EVENT_KEY, 'display_name': 'Synthetic metrics page event'}, expected=(201,))
        draft = self.owner_request('GET', events + '/' + EVENT_KEY + '/draft')
        draft = self.owner_request('PATCH', events + '/' + EVENT_KEY + '/draft', definition(), {'If-Match': '"' + str(draft['revision']) + '"'})
        checked = self.owner_request('POST', events + '/' + EVENT_KEY + '/validate', {'draft_revision': draft['revision'], 'sample_properties': {'page': 'selected'}})
        require(checked['valid'] is True, 'Metrics event draft is not valid')
        release = self.owner_request('POST', self.base + '/releases', {'environment': 'development', 'expected_active_revision': 0,
                                    'events': [{'event_key': EVENT_KEY, 'draft_revision': draft['revision']}]}, {'Idempotency-Key': identifier()}, (202,))
        release_id = resource_id(release['release_id'])
        self.source = self.owner_request('POST', self.base + '/sources', {'name': 'Synthetic metrics server', 'environment': 'development', 'kind': 'server', 'allowed_origins': []}, expected=(201,))

        def ready(value):
            require(value['state'] != 'failed', 'Metrics event release delivery failed')
            return value['state'] == 'published' and value['ingestion_ready'] is True

        release = self.wait(lambda: self.owner_request('GET', self.base + '/releases/' + release_id), ready, 'Real metrics release delivery')
        activation = self.owner_request('POST', self.base + '/release-activations', {'environment': 'development', 'release_id': release_id, 'expected_active_revision': 0}, {'Idempotency-Key': identifier()})
        require(activation['active_release_revision'] == release['revision'], 'Metrics activation selected another release')
        config = self.request(self.public, 'GET', '/v1/config?' + urllib.parse.urlencode({'write_key': self.source['write_key'], 'current_release_revision': 0}), headers=self.source_headers())[0]
        selected = [event for event in config['events'] if event['event_key'] == EVENT_KEY]
        require(len(selected) == 1 and selected[0]['schema_version'] == 1, 'Metrics fixture must pin its first published event version')
        require(config['project_id'] == self.project and config['environment'] == 'development', 'Metrics public config changed scope')
        return release['revision']

    def source_headers(self):
        return {'Authorization': 'Bearer ' + self.source['server_secret_once']}

    def ingest(self, release_revision):
        uids = ['synthetic-metric-' + uuid.uuid4().hex for _ in range(3)]
        facts = []
        for index, (user, page) in enumerate(((0, 'selected'), (0, 'selected'), (1, 'selected'), (2, 'excluded'), (2, 'excluded'))):
            facts.append({'event_id': identifier(), 'event_key': EVENT_KEY, 'schema_version': 1, 'release_revision': release_revision,
                          'occurred_at': timestamp(self.started + timedelta(milliseconds=index)), 'identity': {'uid': uids[user]},
                          'context': {'session_id': identifier(), 'tab_id': identifier(), 'sequence': index + 1, 'page_view_id': identifier()},
                          'sdk': {'name': 'synthetic-metrics-http', 'version': '1.0.0'}, 'properties': {'page': page}})
        for events in (facts, [facts[0]]):
            receipt = self.request(self.public, 'POST', '/v1/collect', {'protocol_version': 1, 'batch_id': identifier(), 'write_key': self.source['write_key'], 'sent_at': timestamp(), 'events': events}, self.source_headers(), (202,))[0]
            require(len(receipt['results']) == len(events) and all(item['status'] == 'accepted' for item in receipt['results']), 'Metrics synthetic facts were not accepted by the real queue')

    def query_result(self, client, bearer, job):
        query_id = resource_id(job['query_id'])

        def ready(value):
            state = value['state']
            require(state in ('queued', 'running', 'succeeded'), 'Metrics query did not complete successfully')
            return state == 'succeeded'

        self.wait(lambda: self.request(client, 'GET', self.base + '/queries/' + query_id, headers=bearer)[0], ready, 'Real metrics query')
        result = self.request(client, 'GET', self.base + '/queries/' + query_id + '/result', headers=bearer)[0]
        require(result['query_id'] == query_id and result['metadata']['accuracy'] == 'exact', 'Metrics query scope or exact accuracy changed')
        require(result['metadata']['coverage']['state'] == 'provisional', 'Metrics query lost honest provisional coverage')
        self.pending_queries.discard(query_id)
        return result

    def enqueue(self, client, path, body, bearer=None, key=None):
        self.query_jobs += 1
        require(self.query_jobs <= 24, 'Metrics query-job budget was exhausted')
        headers = {**(bearer or {}), 'Idempotency-Key': key or identifier()}
        job = self.request(client, 'POST', path, body, headers, (202,))[0]
        self.pending_queries.add(resource_id(job['query_id']))
        return job

    def time_range(self):
        return {'from': timestamp(self.started - timedelta(minutes=1)), 'to': timestamp(self.started + timedelta(minutes=5))}

    def visible(self):
        deadline = min(self.deadline, time.monotonic() + self.timeout)
        for _ in range(16):
            job = self.enqueue(self.owner, self.base + '/queries', {'kind': 'trend', 'environment': 'development', 'timezone': 'UTC', 'time_range': self.time_range(),
                               'subject': 'uid', 'event_versions': [{'event_key': EVENT_KEY, 'schema_version': 1}], 'metrics': [{'op': 'count', 'accuracy': 'exact'}, {'op': 'unique', 'accuracy': 'exact'}], 'limit': 20})
            rows = self.query_result(self.owner, None, job)['rows']
            require(len(rows) <= 1, 'Ungrouped visibility query returned multiple rows')
            counts = (rows[0]['metric_0'], rows[0]['metric_1']) if rows else (0, 0)
            require(0 <= counts[0] <= 5 and 0 <= counts[1] <= 3, 'Metrics visibility included duplicate or unexpected facts')
            if counts == (5, 3):
                return
            require(time.monotonic() < deadline, 'Deduplicated metrics facts were not visible before the deadline')
            time.sleep(0.5)
        raise SmokeFailure('Deduplicated metrics facts exceeded the visibility query budget')

    @staticmethod
    def page_filter(value):
        return {'op': 'eq', 'field': {'kind': 'property', 'event_key': EVENT_KEY, 'schema_version': 1, 'key': 'page'}, 'value': value}

    def metric_definition(self, operation):
        return {'title': '  Synthetic metric ' + operation + '  ', 'description': '', 'event_versions': [{'event_key': EVENT_KEY, 'schema_version': 1}],
                'filter': self.page_filter('selected'), 'subject': 'uid', 'aggregation': {'op': operation, 'accuracy': 'exact'}, 'unit': 'event' if operation == 'count' else 'raw_uid'}

    def create_publish(self, client, definition_value, bearer=None):
        collection = self.base + '/metrics'
        body, create_key = {'environment': 'development', 'definition': definition_value}, identifier()
        headers = {**(bearer or {}), 'Idempotency-Key': create_key}
        draft, response_headers, _ = self.request(client, 'POST', collection, body, headers, (201,))
        metric_id = resource_id(draft['metric_id'])
        path = collection + '/' + metric_id
        require(draft['draft_revision'] == 1 and draft['published_version'] == 0 and response_headers['ETag'] == '"1"', 'Metrics initial revision is wrong')
        checked = self.request(client, 'POST', path + '/validations', {'draft_revision': 1, 'expected_published_version': 0}, bearer)[0]
        require(checked['valid'] is True and checked['execution_performed'] is False, 'Metrics validation unexpectedly executed a query')
        require(checked['normalized_definition']['title'] == definition_value['title'].strip(), 'Metrics validation did not normalize its title')
        require(checked['dependencies']['selection_mode'] == 'event_versions' and len(checked['dependencies']['event_versions']) == 1, 'Metric dependencies did not pin the published event')
        for name in ('definition_hash', 'dependency_hash', 'validation_hash'):
            require(re.fullmatch('[a-f0-9]{64}', checked[name]), 'Metrics validation omitted an exact digest')
        publish_body = {'draft_revision': 1, 'expected_published_version': 0, 'validation_hash': checked['validation_hash']}
        bad_hash = {**publish_body, 'validation_hash': '0' * 64}
        self.request(client, 'POST', path + '/versions', bad_hash, {**(bearer or {}), 'Idempotency-Key': identifier()}, (409,))
        publish_headers = {**(bearer or {}), 'Idempotency-Key': identifier()}
        first = self.request(client, 'POST', path + '/versions', publish_body, publish_headers, (201,))[0]
        require(first['version'] == 1 and all(first[name] == checked[name] for name in ('definition_hash', 'dependency_hash', 'validation_hash')), 'Metrics publication changed its acknowledged hashes')
        require(first['definition'] == checked['normalized_definition'], 'Metrics publication changed the normalized definition')
        return {'path': path, 'draft': draft, 'first': first, 'body': body, 'create_headers': headers, 'publish_body': publish_body, 'publish_headers': publish_headers}

    def revisions(self, metric):
        path, original = metric['path'], metric['first']
        current = self.owner_request('GET', path + '/draft')
        definition_value = {**current['definition'], 'title': 'Synthetic metric count revised'}
        updated, headers, _ = self.request(self.owner, 'PATCH', path + '/draft', {'definition': definition_value}, {'If-Match': '"1"'})
        require(updated['draft_revision'] == 2 and updated['published_version'] == 1 and headers['ETag'] == '"2"', 'Metrics draft CAS did not advance independently')
        self.owner_request('PATCH', path + '/draft', {'definition': definition_value}, {'If-Match': '"1"'}, (409,))
        self.owner_request('POST', path + '/versions', metric['publish_body'], {'Idempotency-Key': identifier()}, (409,))
        checked = self.owner_request('POST', path + '/validations', {'draft_revision': 2, 'expected_published_version': 1})
        require(any(change['path'] == '/title' for change in checked['diff']), 'Metric metadata revision omitted its publication diff')
        second = self.owner_request('POST', path + '/versions', {'draft_revision': 2, 'expected_published_version': 1, 'validation_hash': checked['validation_hash']}, {'Idempotency-Key': identifier()}, (201,))
        require(second['version'] == 2, 'Metrics second version was not published')
        require(self.owner_request('POST', self.base + '/metrics', metric['body'], metric['create_headers'], (201,)) == metric['draft'], 'Metrics create replay returned mutable state')
        require(self.owner_request('POST', path + '/versions', metric['publish_body'], metric['publish_headers'], (201,)) == original, 'Metrics publication replay lost its immutable response')
        require(self.owner_request('GET', path + '/versions/1') == original, 'Published metric version changed after draft edits')
        require(self.owner_request('GET', path)['published_version'] == 2, 'Metrics replay moved its published pointer backwards')
        page = self.owner_request('GET', path + '/versions?limit=1')
        require([item['version'] for item in page['items']] == [1] and page['next_cursor'], 'Metric version pagination omitted its cursor')
        continuation = self.owner_request('GET', path + '/versions?limit=1&cursor=' + urllib.parse.quote(page['next_cursor'], safe=''))
        require([item['version'] for item in continuation['items']] == [2] and continuation['next_cursor'] is None, 'Metric version pagination repeated or skipped a version')

    def issue_machine(self, environment, permissions):
        issued = self.owner_request('POST', self.base + '/machine-principals', {'name': 'Synthetic metrics verifier', 'environment': environment, 'permissions': permissions,
                                    'expires_at': timestamp(datetime.now(timezone.utc) + timedelta(hours=1))}, {'Idempotency-Key': identifier()}, (201,))
        self.machines.append(issued['principal']['id'])
        return issued, HTTP(self.api), {'Authorization': 'Bearer ' + issued['secret_once']}

    def execute(self, metric, client, bearer, runtime_filter, expected, replay=False):
        path = metric['path'] + '/versions/1/queries'
        body = {'time_range': self.time_range(), 'timezone': 'UTC', 'filter': runtime_filter, 'limit': 20}
        # Counts are additive across day buckets. Exact UV is tested over the
        # whole window, because adding daily unique values changes its meaning.
        unique = metric['first']['definition']['aggregation']['op'] == 'unique'
        if not unique:
            body['interval'] = 'day'
        key = identifier()
        job = self.enqueue(client, path, body, bearer, key)
        if replay:
            retry = self.request(client, 'POST', path, body, {**(bearer or {}), 'Idempotency-Key': key}, (202,))[0]
            require(retry['query_id'] == job['query_id'], 'Metrics execution retry queued another query')
            self.request(client, 'POST', metric['path'] + '/versions/2/queries', body, {**(bearer or {}), 'Idempotency-Key': key}, (409,))
        result = self.query_result(client, bearer, job)
        rows = result['rows']
        require(all(isinstance(row.get('metric_0'), (int, float)) and not isinstance(row['metric_0'], bool) for row in rows), 'Metric result contains invalid numeric values')
        if unique:
            require(len(rows) == 1, 'Whole-window metric UV returned multiple rows')
        require(sum(row['metric_0'] for row in rows) == expected, 'Metric fixed and runtime filters produced an unexpected result')
        if replay:
            retry = self.request(client, 'POST', path, body, {**(bearer or {}), 'Idempotency-Key': key}, (202,))[0]
            require(retry['query_id'] == job['query_id'] and retry['state'] == 'succeeded', 'Completed metrics replay omitted current query state')
        return job

    def integration_helper(self, issued, metric):
        helper = Path(__file__).resolve().parents[2] / 'integrations/tooling/activity_api.py'
        with tempfile.TemporaryDirectory(prefix='evia-metrics-helper-') as directory:
            output = Path(directory) / 'version.json'
            self.calls += 2  # The real helper first reads identity, then the resource.
            require(self.calls <= 1000 and time.monotonic() < self.deadline, 'Metrics helper exhausted its request/time budget')
            try:
                result = subprocess.run([sys.executable, str(helper), '--endpoint', self.api, '--allow-loopback-http', '--project', self.project,
                                         '--environment', 'development', '--path', metric['path'] + '/versions/1', '--output', str(output)],
                                        env={**os.environ, 'EVIA_MACHINE_TOKEN': issued['secret_once']}, capture_output=True, timeout=45, check=False)
            except (OSError, subprocess.TimeoutExpired):
                raise SmokeFailure('Metrics integration helper could not complete') from None
            require(result.returncode == 0, 'Metrics integration helper failed; response and diagnostics omitted')
            require(output.is_file() and output.stat().st_size <= 512 * 1024 and output.stat().st_mode & 0o077 == 0, 'Metrics helper did not produce a bounded private response')
            require(json.loads(output.read_text()) == metric['first'], 'Integration helper changed the immutable metric version')

    def denied_routes(self, metric, client, bearer):
        path = metric['path']
        routes = [('GET', self.base + '/metrics?environment=development', None), ('GET', path, None), ('GET', path + '/draft', None),
                  ('GET', path + '/versions', None), ('GET', path + '/versions/1', None),
                  ('POST', self.base + '/metrics', metric['body']), ('PATCH', path + '/draft', {'definition': metric['first']['definition']}),
                  ('POST', path + '/validations', {'draft_revision': 2, 'expected_published_version': 2}),
                  ('POST', path + '/versions', {'draft_revision': 2, 'expected_published_version': 2, 'validation_hash': metric['first']['validation_hash']}),
                  ('POST', path + '/versions/1/queries', {'time_range': self.time_range(), 'timezone': 'UTC', 'interval': 'day'})]
        for method, route, body in routes:
            problem = self.request(client, method, route, body, {**bearer, 'Idempotency-Key': identifier(), 'If-Match': '"2"'}, (403,))[0]
            require(problem['code'] == 'MACHINE_FORBIDDEN', 'Metric machine denial returned an unexpected authorization code')

    def machine_audit(self, issued, metric):
        filters = {'from': timestamp(self.started), 'to': timestamp(datetime.now(timezone.utc) + timedelta(seconds=1)),
                   'actor_id': issued['principal']['id'], 'action': 'metric.created', 'limit': 100}
        entries = self.owner_request('GET', self.base + '/audit-logs?' + urllib.parse.urlencode(filters))
        require(len(entries['items']) == 1 and entries['next_cursor'] is None, 'Metric machine create replay duplicated audit')
        entry = entries['items'][0]
        require(entry['actor_kind'] == 'machine' and entry['actor_id'] == issued['principal']['id']
                and entry['credential_id'] == issued['credential']['id'] and entry['project_id'] == self.project
                and entry['resource_key'] == metric['first']['metric_id'], 'Metric machine audit lost exact credential provenance')

    def cleanup(self):
        # Only resources created by this invocation. Preserve account/project
        # fixtures and their immutable evidence; revoke live access and ingestion.
        for query_id in self.pending_queries:
            self.owner_request('DELETE', self.base + '/queries/' + query_id, expected=(202,))
        self.pending_queries.clear()
        for principal in self.machines:
            path = self.base + '/machine-principals/' + principal
            detail = self.owner_request('GET', path)
            if detail['principal']['revoked_at'] is None:
                self.owner_request('DELETE', path, headers={'If-Match': '"' + str(detail['principal']['revision']) + '"'})
        if self.source:
            path = self.base + '/sources/' + self.source['id']
            current = self.owner_request('GET', path)
            self.owner_request('PATCH', path, {'status': 'disabled'}, {'If-Match': '"' + str(current['revision']) + '"'})
        if self.logged_in:
            self.owner_request('DELETE', '/api/v1/session', expected=(204,))
            self.logged_in = False

    def run(self):
        completed, cleanup_ok = False, False
        try:
            self.provision()
            self.ingest(self.publish_event())
            self.visible()
            count = self.create_publish(self.owner, self.metric_definition('count'))
            self.revisions(count)
            invalid = self.metric_definition('count')
            invalid['unit'] = 'raw_uid'
            problem = self.owner_request('POST', self.base + '/metrics', {'environment': 'development', 'definition': invalid}, {'Idempotency-Key': identifier()}, (400,))
            require(problem['code'] == 'VALIDATION_FAILED', 'Incompatible metric unit was not rejected by the authoritative request schema')
            issued, machine, bearer = self.issue_machine('development', PERMISSIONS)
            unique = self.create_publish(machine, self.metric_definition('unique'), bearer)
            require(self.request(machine, 'GET', unique['path'], headers=bearer)[0]['published_version'] == 1, 'Explicit metric read scope did not authorize resource reads')
            require(self.request(machine, 'POST', self.base + '/metrics', unique['body'], unique['create_headers'], (201,))[0] == unique['draft'], 'Machine metric create replay changed its response')
            machine_draft = self.request(machine, 'GET', unique['path'] + '/draft', headers=bearer)[0]
            machine_definition = {**machine_draft['definition'], 'title': 'Synthetic machine metric revised draft'}
            saved = self.request(machine, 'PATCH', unique['path'] + '/draft', {'definition': machine_definition}, {**bearer, 'If-Match': '"1"'})[0]
            require(saved['draft_revision'] == 2 and saved['published_version'] == 1, 'Explicit metric write scope did not authorize draft CAS')
            versions = self.request(machine, 'GET', unique['path'] + '/versions', headers=bearer)[0]
            require([item['version'] for item in versions['items']] == [1], 'Explicit metric read scope did not authorize version listing')
            machine_list = self.request(machine, 'GET', self.base + '/metrics?environment=development', headers=bearer)[0]
            require(len(machine_list['items']) == 2, 'Explicit metric read scope did not authorize environment listing')
            page = self.owner_request('GET', self.base + '/metrics?environment=development&limit=1')
            require(len(page['items']) == 1 and page['next_cursor'], 'Metric list pagination was not exercised')
            next_page = self.owner_request('GET', self.base + '/metrics?environment=development&limit=1&cursor=' + urllib.parse.quote(page['next_cursor'], safe=''))
            require(len(next_page['items']) == 1 and next_page['items'][0]['metric_id'] != page['items'][0]['metric_id'] and next_page['next_cursor'] is None, 'Metric list cursor skipped or duplicated a resource')
            require(not self.owner_request('GET', self.base + '/metrics?environment=staging')['items'], 'Metrics list leaked into another environment')
            self.request(self.public, 'GET', count['path'], expected=(401,))
            broad = {'op': 'or', 'args': [self.page_filter('selected'), self.page_filter('excluded')]}
            self.execute(count, self.owner, None, broad, 3, replay=True)
            self.execute(count, self.owner, None, self.page_filter('excluded'), 0)
            job = self.execute(unique, machine, bearer, broad, 2)
            _, old, old_bearer = self.issue_machine('development', ['analyses.read', 'analyses.write', 'analyses.publish', 'analyses.run', 'queries.read', 'queries.run'])
            self.denied_routes(count, old, old_bearer)
            _, wrong, wrong_bearer = self.issue_machine('staging', PERMISSIONS)
            self.denied_routes(count, wrong, wrong_bearer)
            self.machine_audit(issued, unique)
            self.integration_helper(issued, unique)
            rotated = self.owner_request('POST', self.base + '/machine-principals/' + issued['principal']['id'] + '/credential-rotations',
                                         {'expires_at': timestamp(datetime.now(timezone.utc) + timedelta(hours=1)), 'overlap_seconds': 60},
                                         {'If-Match': '"1"', 'Idempotency-Key': identifier()}, (201,))
            self.owner_request('DELETE', self.base + '/machine-principals/' + issued['principal']['id'] + '/credentials/' + issued['credential']['id'], headers={'If-Match': '"2"'})
            for route in (unique['path'], self.base + '/queries/' + job['query_id'] + '/result', '/api/v1/machine-identity'):
                self.request(machine, 'GET', route, headers=bearer, expected=(401,))
            self.request(machine, 'POST', unique['path'] + '/versions/1/queries', {'time_range': self.time_range(), 'timezone': 'UTC', 'interval': 'day'}, {**bearer, 'Idempotency-Key': identifier()}, (401,))
            replacement = {'Authorization': 'Bearer ' + rotated['secret_once']}
            require(self.request(machine, 'GET', unique['path'] + '/versions/1', headers=replacement)[0] == unique['first'], 'Exact credential revocation disabled the replacement credential')
            completed = True
        finally:
            # Give cleanup its own small bounded window even after a phase timeout.
            self.deadline = time.monotonic() + 60
            try:
                self.cleanup()
                cleanup_ok = True
            except (SmokeFailure, KeyError, ValueError):
                if completed:
                    raise SmokeFailure('Metrics verification cleanup failed; inspect private runtime state') from None
        require(completed and cleanup_ok, 'Metrics verification did not complete')
        print(json.dumps({'stage': 'independent_metrics', 'status': 'passed', 'http_requests': self.calls, 'query_jobs': self.query_jobs,
                          'provisioned_accounts': 1, 'accepted_submissions': 6, 'deduplicated_facts': 5, 'total_raw_uid_unique': 3,
                          'fixed_page_event_count': 3, 'fixed_page_raw_uid_unique': 2, 'conflicting_runtime_filter_count': 0,
                          'immutable_count_versions': 2, 'machine_scopes': True, 'old_analysis_scope_denials': 10,
                          'wrong_environment_denials': 10, 'exact_credential_revocation': True, 'audit_credential_provenance': True,
                          'integration_helper_read': True, 'whole_window_unique': True,
                          'original_retry_snapshots': True, 'cleanup_completed': cleanup_ok}, sort_keys=True))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--api-url', default='http://127.0.0.1:8080')
    parser.add_argument('--console-origin', default='http://127.0.0.1:5173')
    parser.add_argument('--binary', required=True)
    parser.add_argument('--runtime-env', help='Optional private mode-0600 synthetic runtime environment file')
    parser.add_argument('--timeout', type=int, default=60)
    args = parser.parse_args()
    require(5 <= args.timeout <= 120, 'Metrics phase timeout must be between 5 and 120 seconds')
    if args.runtime_env:
        _, settings = load(args.runtime_env)
        os.environ.update(settings)
    MetricsSmoke(args.api_url, args.console_origin, args.binary, args.timeout).run()


if __name__ == '__main__':
    try:
        main()
    except (SmokeFailure, KeyError, ValueError, TypeError, IndexError, OSError) as exc:
        print(str(exc) if isinstance(exc, SmokeFailure) else 'Metrics verification received invalid configuration or response data', file=sys.stderr)
        raise SystemExit(1) from None
