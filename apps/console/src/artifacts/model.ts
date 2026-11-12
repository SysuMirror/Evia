import type { TypeScriptArtifact } from '@evia/api-client';

export function artifactFileName(releaseID: string): string {
  return `events-${releaseID}.ts`;
}

export function formatByteSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}

/**
 * Triggers a client-side download without any extra round trip. The generated
 * source is already fully present in the artifact response.
 */
export function downloadTypeScript(artifact: TypeScriptArtifact): void {
  const blob = new Blob([artifact.source], { type: 'text/typescript;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = artifactFileName(artifact.release_id);
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}
