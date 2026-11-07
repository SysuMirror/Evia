package budget

// Integer micro-events preserve fractional refill without rounding allowances up.
// Redis runs the complete read/refill/check/debit/expiry transaction atomically.
const reserveLua = `
local capacity = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])
local cost = tonumber(ARGV[3])
local idle = tonumber(ARGV[4])
local max_skew = tonumber(ARGV[5])
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local state = redis.call('HMGET', KEYS[1], 'tokens', 'at', 'capacity', 'rate')
local tokens = capacity
local last = now
if state[1] or state[2] or state[3] or state[4] then
  tokens = tonumber(state[1])
  last = tonumber(state[2])
  if not tokens or not last or tokens < 0 or tokens > capacity or tokens % 1 ~= 0 or last < 0 or last % 1 ~= 0 then
    return redis.error_reply('INVALID_COLLECTION_BUDGET_STATE')
  end
  if tonumber(state[3]) ~= capacity or tonumber(state[4]) ~= rate then
    return redis.error_reply('COLLECTION_BUDGET_POLICY_MISMATCH')
  end
end
local elapsed = now - last
local skew = 0
if elapsed < 0 then
  skew = -elapsed
  if skew > max_skew then return redis.error_reply('INVALID_COLLECTION_BUDGET_CLOCK') end
  elapsed = 0
  now = last
end
-- Clamp before multiplication, so even a long idle period stays below 2^53.
elapsed = math.min(elapsed, idle)
tokens = math.min(capacity, tokens + elapsed * rate)
local allowed = 0
if tokens >= cost then
  tokens = tokens - cost
  allowed = 1
end
redis.call('HSET', KEYS[1], 'tokens', tokens, 'at', now, 'capacity', capacity, 'rate', rate)
-- A backward wall-clock movement must not expire a bucket before full refill.
redis.call('PEXPIRE', KEYS[1], idle + skew)
return allowed
`
