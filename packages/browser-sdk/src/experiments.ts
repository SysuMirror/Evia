import type { ConfigExperiment, ExperimentAssignmentReason } from './types.js';

const FNV_OFFSET_BASIS = 0x811c9dc5;
const FNV_PRIME = 0x01000193;

/**
 * Deterministic 32-bit FNV-1a hash mapped uniformly into [0, 9999] basis points.
 * Uses UTF-8 bytes of `${salt}:${experimentKey}:${subjectId}` so browser and
 * server runtimes produce identical assignments with zero dependencies.
 */
export function hashExperimentBucketBps(
  salt: string,
  experimentKey: string,
  subjectId: string,
): number {
  const input = `${salt}:${experimentKey}:${subjectId}`;
  const bytes = new TextEncoder().encode(input);
  let hash = FNV_OFFSET_BASIS;
  for (let i = 0; i < bytes.length; i++) {
    hash ^= bytes[i]!;
    hash = Math.imul(hash, FNV_PRIME) >>> 0;
  }
  return hash % 10000;
}

export function assignExperimentVariant(
  experiment: ConfigExperiment,
  subjectId: string,
): {
  variant: string;
  allocated: boolean;
  reason: Exclude<ExperimentAssignmentReason, 'not_found'>;
} {
  const override = experiment.overrides?.[subjectId];
  if (override && experiment.variants.some((v) => v.key === override)) {
    return { variant: override, allocated: true, reason: 'override' };
  }
  const bucket = hashExperimentBucketBps(experiment.salt, experiment.experiment_key, subjectId);
  let cursor = 0;
  for (const variant of experiment.variants) {
    cursor += variant.weight_bps;
    if (bucket < cursor) {
      return { variant: variant.key, allocated: true, reason: 'allocated' };
    }
  }
  return { variant: experiment.control_variant, allocated: false, reason: 'unallocated' };
}
