-- Patreon memberships have an explicit active state (patron_status), unlike
-- Ko-fi which only notifies on payment. Track it so an ended membership can be
-- deactivated without deleting the event.
ALTER TABLE donation_events ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT TRUE;
