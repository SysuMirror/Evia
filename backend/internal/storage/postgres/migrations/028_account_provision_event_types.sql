-- Version 028: type the account provision audit trail.
-- Every existing row was written by ProvisionAccount, the only historical
-- insert site, so backfilling 'account_created' is provably exact. The column
-- default is dropped afterwards so future inserts must name the event type
-- explicitly: an unlabelled operator action fails at the database instead of
-- silently masquerading as an account creation.
ALTER TABLE account_provision_events
  ADD COLUMN event_type text NOT NULL DEFAULT 'account_created';
ALTER TABLE account_provision_events
  ADD CONSTRAINT account_provision_events_type_check
  CHECK(event_type IN
    ('account_created','account_disabled','account_enabled','password_reset'));
ALTER TABLE account_provision_events ALTER COLUMN event_type DROP DEFAULT;
