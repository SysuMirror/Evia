-- Nested composite metrics: a formula version may directly pin another
-- formula_metric_version, not only atomic metric_versions. The witness keeps
-- its generic identity columns (ref_metric_id/ref_version) and gains two
-- nullable foreign-key targets, exactly one of which must be populated.
-- Existing witnesses all pin atomic versions and are backfilled accordingly.

-- The original constraint used the generated name, so locate it dynamically.
DO $$
DECLARE
 witness_fk text;
BEGIN
 SELECT conname INTO witness_fk
  FROM pg_constraint
  WHERE conrelid = 'formula_metric_dependencies'::regclass
   AND contype = 'f'
   AND confrelid = 'metric_versions'::regclass;
 IF witness_fk IS NULL THEN
  RAISE EXCEPTION 'formula metric dependency atomic foreign key not found' USING ERRCODE='23514';
 END IF;
 EXECUTE 'ALTER TABLE formula_metric_dependencies DROP CONSTRAINT ' || quote_ident(witness_fk);
END
$$;

ALTER TABLE formula_metric_dependencies
 ADD COLUMN ref_atomic_metric_id uuid,
 ADD COLUMN ref_atomic_version bigint,
 ADD COLUMN ref_formula_metric_id uuid,
 ADD COLUMN ref_formula_version bigint;

-- Every historical witness was written through the atomic-only foreign key.
UPDATE formula_metric_dependencies
 SET ref_atomic_metric_id = ref_metric_id,
     ref_atomic_version = ref_version;

ALTER TABLE formula_metric_dependencies
 ADD CONSTRAINT formula_metric_dependencies_atomic_ref_fkey
  FOREIGN KEY(project_id,environment,ref_atomic_metric_id,ref_atomic_version)
  REFERENCES metric_versions(project_id,environment,metric_id,version),
 ADD CONSTRAINT formula_metric_dependencies_formula_ref_fkey
  FOREIGN KEY(project_id,environment,ref_formula_metric_id,ref_formula_version)
  REFERENCES formula_metric_versions(project_id,environment,metric_id,version),
 ADD CONSTRAINT formula_metric_dependencies_ref_target_sound CHECK(
  (
   ref_atomic_metric_id IS NOT NULL AND ref_atomic_version IS NOT NULL
   AND ref_formula_metric_id IS NULL AND ref_formula_version IS NULL
   AND ref_atomic_metric_id = ref_metric_id AND ref_atomic_version = ref_version
  ) OR (
   ref_formula_metric_id IS NOT NULL AND ref_formula_version IS NOT NULL
   AND ref_atomic_metric_id IS NULL AND ref_atomic_version IS NULL
   AND ref_formula_metric_id = ref_metric_id AND ref_formula_version = ref_version
  )
 );
