/** Parse exact browser Origins without silently accepting paths or credentials. */
export function parseSourceOrigins(text: string): { origins: string[]; error?: string } {
  const lines = text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
  if (!lines.length) return { origins: [], error: '至少填写一个 Origin' };
  if (lines.length > 50) return { origins: [], error: '最多填写 50 个 Origin' };
  const origins: string[] = [];
  for (const line of lines) {
    if (line.length > 2048 || !/^https?:\/\/[^/?#]+$/.test(line) || /[^\x21-\x7e]|[\\*]/.test(line))
      return { origins: [], error: '请填写完整 http(s) Origin，不含路径、查询、通配符或凭据' };
    try {
      const parsed = new URL(line);
      if (
        parsed.username ||
        parsed.password ||
        !parsed.hostname ||
        parsed.origin === 'null' ||
        /:\s*$/.test(line)
      )
        return { origins: [], error: 'Origin 的主机、端口或凭据无效' };
      // Browser URL parsing canonicalizes host case and default ports.
      origins.push(parsed.origin);
    } catch {
      return { origins: [], error: 'Origin 的主机或端口无效，端口范围为 1 到 65535' };
    }
  }
  if (origins.some((origin) => /:0$/.test(origin)))
    return { origins: [], error: 'Origin 端口范围为 1 到 65535' };
  if (new Set(origins).size !== origins.length)
    return { origins: [], error: 'Origin 不可重复，默认端口与省略端口视为相同' };
  return { origins: origins.sort() };
}
