import { expect, test } from '@playwright/test';
import { parseSourceOrigins } from '../../apps/console/src/source-origins';

test('source Origins preserve browser scheme, host and port boundaries', () => {
  expect(
    parseSourceOrigins('https://APP.example:443\nhttp://localhost:3000\nhttps://[::1]:8443')
      .origins,
  ).toEqual(['http://localhost:3000', 'https://[::1]:8443', 'https://app.example']);
  expect(parseSourceOrigins('https://app.example\nhttps://app.example:444').error).toBeUndefined();
  expect(parseSourceOrigins('http://app.example\nhttps://app.example').error).toBeUndefined();
  expect(parseSourceOrigins('https://xn--bcher-kva.example').error).toBeUndefined();
});

test('source Origins reject URL features that broaden or misstate an allowlist', () => {
  for (const invalid of [
    'https://app.example/',
    'https://app.example/path',
    'https://app.example?key=value',
    'https://app.example#fragment',
    'https://user:secret@app.example',
    'https://*.example',
    'https://app.example\\other',
    'https://bücher.example',
    'ftp://app.example',
    '//app.example',
    'null',
    'https://app.example:0',
    'https://app.example:65536',
    'https://app.example:',
    'https://app.example:abc',
  ]) {
    expect(parseSourceOrigins(invalid).error, invalid).toBeTruthy();
    expect(parseSourceOrigins(invalid).origins, invalid).toEqual([]);
  }
});

test('source Origins reject duplicate browser identities and bound allowlist size', () => {
  for (const duplicate of [
    'https://app.example\nhttps://app.example',
    'https://APP.example\nhttps://app.example:443',
    'http://app.example:80\nhttp://app.example',
  ])
    expect(parseSourceOrigins(duplicate).error, duplicate).toBeTruthy();
  expect(parseSourceOrigins(' \n\n ').error).toBeTruthy();
  const hosts = Array.from({ length: 50 }, (_, index) => `https://host-${index}.example`);
  expect(parseSourceOrigins(hosts.join('\n')).origins).toHaveLength(50);
  expect(parseSourceOrigins([...hosts, 'https://extra.example'].join('\n')).error).toBeTruthy();
  expect(parseSourceOrigins(`https://${'a'.repeat(2048)}.example`).error).toBeTruthy();
});
