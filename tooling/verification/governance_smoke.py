#!/usr/bin/env python3
"""Verify project governance against a migrated, synthetic local runtime."""
import argparse
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time
import urllib.parse
import uuid

from backend_smoke import HTTP, SmokeFailure, require, resource_id, timestamp


class GovernanceSmoke:
    def __init__(self, api, origin, binary):
        target = urllib.parse.urlsplit(api)
        require(target.scheme == 'http' and target.hostname in ('127.0.0.1', 'localhost', '::1') and not target.username and not target.password and target.path in ('', '/') and not target.query and not target.fragment, 'Governance verification requires an explicit loopback HTTP runtime')
        require(Path(binary).is_file(), 'A built backend binary is required')
        self.api, self.origin, self.binary = api, origin, binary
        self.calls, self.deadline = 0, time.monotonic() + 180
        self.started = datetime.now(timezone.utc) - timedelta(seconds=5)
        self.owner = HTTP(api, origin, session=True)
        self.project = None

    def request(self, client, method, path, body=None, headers=None, expected=(200,)):
        self.calls += 1
        require(self.calls <= 100 and time.monotonic() < self.deadline, 'Governance verification exhausted its request/time budget')
        value, response_headers, status = client.request(method, path, body, headers, expected)
        require('no-store' in response_headers.get('Cache-Control', ''), 'Governance response lacks no-store')
        return value, response_headers, status

    def login(self, client, username, password):
        value, _, _ = self.request(client, 'POST', '/api/v1/session/login', {'username': username, 'password': password})
        client.csrf = value['csrf_token']
        return resource_id(value['user_id'])

    def provision(self, role):
        username = 'governance-' + role + '-' + uuid.uuid4().hex[:16]
        password = secrets.token_urlsafe(24)
        env = {**os.environ, 'EVIA_ACCOUNT_USERNAME': username, 'EVIA_ACCOUNT_DISPLAY_NAME': 'Synthetic governance ' + role, 'EVIA_ACCOUNT_PASSWORD': password}
        try:
            result = subprocess.run([self.binary, 'account-create'], env=env, capture_output=True, timeout=30, check=False)
        except (OSError, subprocess.TimeoutExpired):
            raise SmokeFailure('Account provisioning could not complete') from None
        require(result.returncode == 0, 'Account provisioning failed; inspect private operator diagnostics')
        # Never forward subprocess output: it is not a secret-safe evidence channel.
        client = HTTP(self.api, self.origin, session=True)
        user_id = self.login(client, username, password)
        return {'username': username, 'id': user_id, 'client': client}

    @staticmethod
    def mutation(revision, key=None):
        return {'If-Match': '"' + str(revision) + '"', 'Idempotency-Key': key or str(uuid.uuid4())}

    def run(self):
        primary = self.provision('primary')
        self.owner, root_id = primary['client'], primary['id']
        editor, second = self.provision('editor'), self.provision('owner')
        project, _, _ = self.request(self.owner, 'POST', '/api/v1/projects', {'name': 'Synthetic governance ' + uuid.uuid4().hex[:8], 'timezone': 'UTC'}, expected=(201,))
        self.project = resource_id(project['id'])
        base = '/api/v1/projects/' + self.project
        roster, headers, _ = self.request(self.owner, 'GET', base + '/members')
        require(roster['revision'] == 1 and len(roster['items']) == 1 and headers['ETag'] == '"1"', 'Initial membership revision is wrong')
        denied, _, _ = self.request(self.owner, 'DELETE', base + '/members/' + root_id, headers=self.mutation(1), expected=(409,))
        require(denied['code'] == 'LAST_OWNER_REQUIRED', 'Last enabled owner removal was not rejected')
        add_headers = self.mutation(1)
        body = {'username': editor['username'], 'role': 'editor'}
        added, _, _ = self.request(self.owner, 'POST', base + '/members', body, add_headers, (201,))
        replay, _, _ = self.request(self.owner, 'POST', base + '/members', body, add_headers, (201,))
        require(added == replay and added['revision'] == 2 and added['member']['user_id'] == editor['id'], 'Member replay changed the accepted operation')
        page, _, _ = self.request(self.owner, 'GET', base + '/members?limit=1')
        require(page['next_cursor'] is not None, 'Member pagination was not exercised')
        second_page, _, _ = self.request(self.owner, 'GET', base + '/members?limit=1&cursor=' + urllib.parse.quote(page['next_cursor'], safe=''))
        require(page['items'][0]['user_id'] != second_page['items'][0]['user_id'], 'Member cursor duplicated an item')
        self.request(editor['client'], 'GET', base + '/members', expected=(403,))
        settings, _, _ = self.request(editor['client'], 'GET', base + '/settings')
        self.request(editor['client'], 'PATCH', base + '/settings', {'name': 'Denied edit', 'timezone': 'UTC'}, self.mutation(settings['revision']), (403,))
        update_headers = self.mutation(settings['revision'])
        settings_body = {'name': 'Synthetic governance updated', 'timezone': 'Asia/Shanghai'}
        updated, headers, _ = self.request(self.owner, 'PATCH', base + '/settings', settings_body, update_headers)
        replay, _, _ = self.request(self.owner, 'PATCH', base + '/settings', settings_body, update_headers)
        require(updated == replay and updated['revision'] == 2 and headers['ETag'] == '"2"', 'Settings retry or revision is wrong')
        self.request(self.owner, 'PATCH', base + '/settings', settings_body, self.mutation(1), (409,))
        project, _, _ = self.request(editor['client'], 'GET', base)
        require(project['name'] == settings_body['name'] and project['timezone'] == 'Asia/Shanghai', 'Project reads did not observe saved settings')
        added_owner, _, _ = self.request(self.owner, 'POST', base + '/members', {'username': second['username'], 'role': 'owner'}, self.mutation(2), (201,))
        self.request(self.owner, 'GET', base + '/members?limit=1&cursor=' + urllib.parse.quote(page['next_cursor'], safe=''), expected=(409,))
        demoted, _, _ = self.request(self.owner, 'PATCH', base + '/members/' + editor['id'], {'role': 'viewer'}, self.mutation(added_owner['revision']))
        # This existing domain operation must immediately observe the new role.
        self.request(editor['client'], 'POST', base + '/event-definitions', {'event_key': 'denied', 'display_name': 'Denied after demotion'}, expected=(403,))
        removed, _, _ = self.request(self.owner, 'DELETE', base + '/members/' + editor['id'], headers=self.mutation(demoted['revision']))
        require(removed['member']['user_id'] == editor['id'], 'Removal receipt refers to a different member')
        self.request(editor['client'], 'GET', base + '/settings', expected=(404,))
        self.request(editor['client'], 'GET', base, expected=(404,))
        until = datetime.now(timezone.utc) + timedelta(seconds=1)
        filters = {'from': timestamp(self.started), 'to': timestamp(until), 'limit': '2'}
        audit_path = base + '/audit-logs?' + urllib.parse.urlencode(filters)
        issued, _, _ = self.request(self.owner, 'POST', base + '/machine-principals', {'name': 'Synthetic governance denial', 'environment': 'development', 'permissions': ['project.read'], 'expires_at': timestamp(datetime.now(timezone.utc) + timedelta(hours=1))}, {'Idempotency-Key': str(uuid.uuid4())}, (201,))
        machine = HTTP(self.api)
        bearer = {'Authorization': 'Bearer ' + issued['secret_once']}
        identity, _, _ = self.request(machine, 'GET', '/api/v1/machine-identity', headers=bearer)
        require(identity['principal_id'] == issued['principal']['id'], 'Machine control credential was not valid')
        for route in (base + '/members', base + '/settings', audit_path):
            self.request(machine, 'GET', route, headers=bearer, expected=(401,))
        audit, _, _ = self.request(self.owner, 'GET', audit_path)
        require(audit['next_cursor'] and len(audit['items']) == 2, 'Audit pagination was not exercised')
        continuation, _, _ = self.request(self.owner, 'GET', audit_path + '&cursor=' + urllib.parse.quote(audit['next_cursor'], safe=''))
        require(set(x['id'] for x in audit['items']).isdisjoint(x['id'] for x in continuation['items']), 'Audit cursor duplicated entries')
        for entry in audit['items'] + continuation['items']:
            require(set(entry) == {'id', 'project_id', 'actor_id', 'actor_kind', 'credential_id', 'action', 'resource_key', 'occurred_at'}, 'Unexpected audit data exposed')
            require(entry['project_id'] == self.project and entry['credential_id'] is None, 'Audit scope or human attribution is wrong')
        self.request(self.owner, 'GET', audit_path + '&action=project.created&cursor=' + urllib.parse.quote(audit['next_cursor'], safe=''), expected=(400,))
        filtered, _, _ = self.request(self.owner, 'GET', base + '/audit-logs?' + urllib.parse.urlencode({**filters, 'action': 'project.settings_updated', 'actor_id': root_id}))
        require(len(filtered['items']) == 1 and filtered['next_cursor'] is None, 'Settings retry duplicated audit or exact filter is wrong')
        # Self-demotion succeeds only with another owner and revokes old retry authority.
        self_headers = self.mutation(removed['revision'])
        self.request(self.owner, 'PATCH', base + '/members/' + root_id, {'role': 'editor'}, self_headers)
        self.request(self.owner, 'PATCH', base + '/members/' + root_id, {'role': 'editor'}, self_headers, (403,))
        self.request(self.owner, 'PATCH', base + '/settings', settings_body, update_headers, (403,))
        self.request(self.owner, 'GET', audit_path, expected=(403,))
        current, _, _ = self.request(second['client'], 'GET', base + '/members')
        require(sum(x['role'] == 'owner' and not x['disabled'] for x in current['items']) == 1, 'Unexpected enabled owner count')
        self.request(second['client'], 'DELETE', base + '/members/' + second['id'], headers=self.mutation(current['revision']), expected=(409,))
        self.request(second['client'], 'GET', audit_path)
        self.request(second['client'], 'DELETE', base + '/machine-principals/' + issued['principal']['id'], headers={'If-Match': '"1"'})
        for client in (self.owner, editor['client'], second['client']):
            self.request(client, 'DELETE', '/api/v1/session', expected=(204,))
        print(json.dumps({'stage': 'project_governance', 'status': 'passed', 'http_requests': self.calls, 'provisioned_accounts': 3, 'project_revision_checks': True, 'audit_pagination': True, 'current_role_retry_denial': True}, sort_keys=True))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--api-url', default='http://127.0.0.1:8080')
    parser.add_argument('--console-origin', default='http://127.0.0.1:5173')
    parser.add_argument('--binary', required=True)
    args = parser.parse_args()
    require(bool(os.environ.get('EVIA_DATABASE_URL')), 'Dedicated synthetic database environment is required')
    GovernanceSmoke(args.api_url, args.console_origin, args.binary).run()


if __name__ == '__main__':
    try:
        main()
    except (SmokeFailure, KeyError, ValueError) as exc:
        if isinstance(exc, SmokeFailure):
            print(str(exc), file=sys.stderr)
        else:
            print('Governance verification received invalid configuration or response data', file=sys.stderr)
        raise SystemExit(1) from None
