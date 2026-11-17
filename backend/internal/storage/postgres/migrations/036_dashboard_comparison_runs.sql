-- Comparison runs fan out one additional shifted-window query per widget.
-- Each run widget therefore has up to two rows distinguished by period, while
-- historical rows keep their 'current' default and all existing constraints.

ALTER TABLE dashboard_run_widgets
 ADD COLUMN period text NOT NULL DEFAULT 'current'
 CHECK(period IN ('current','comparison'));

-- The primary key widens to include period so current and comparison rows can
-- coexist for the same widget. Drop the constraint by discovered name to stay
-- independent of PostgreSQL's generated naming.
DO $$
DECLARE
  constraint_name text;
BEGIN
  SELECT conname INTO constraint_name
  FROM pg_constraint
  WHERE conrelid = 'dashboard_run_widgets'::regclass
    AND contype = 'p';
  EXECUTE format('ALTER TABLE dashboard_run_widgets DROP CONSTRAINT %I', constraint_name);
END $$;
ALTER TABLE dashboard_run_widgets
 ADD CONSTRAINT dashboard_run_widgets_pkey
 PRIMARY KEY(project_id, run_id, widget_id, period);

-- Position uniqueness now applies to the current-period row only; comparison
-- rows intentionally share the widget position.
DO $$
DECLARE
  constraint_name text;
BEGIN
  SELECT conname INTO constraint_name
  FROM pg_constraint
  WHERE conrelid = 'dashboard_run_widgets'::regclass
    AND contype = 'u'
    AND pg_get_constraintdef(oid) LIKE '%position%';
  IF constraint_name IS NOT NULL THEN
    EXECUTE format('ALTER TABLE dashboard_run_widgets DROP CONSTRAINT %I', constraint_name);
  END IF;
END $$;
CREATE UNIQUE INDEX dashboard_run_widgets_position_current
 ON dashboard_run_widgets(project_id, run_id, position)
 WHERE period = 'current';

-- Query lookups must find both periods when polling or expiring results.
DROP INDEX IF EXISTS dashboard_run_widgets_query;
CREATE INDEX dashboard_run_widgets_query
 ON dashboard_run_widgets(project_id, environment, query_id)
 WHERE query_id IS NOT NULL;
