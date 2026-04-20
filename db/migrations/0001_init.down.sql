-- IMPL-0002 Phase 1: reverse of 0001_init.up.sql.
--
-- golang-migrate requires paired up/down files. Our production policy
-- is forward-only (roll a new migration to fix issues), but down.sql
-- is useful for local dev resets. Drop in reverse-dependency order.

DROP TABLE IF EXISTS jobs;
DROP TABLE IF EXISTS discussion_participants;
DROP TABLE IF EXISTS discussions;
DROP TABLE IF EXISTS links;
DROP TABLE IF EXISTS authors;
DROP TABLE IF EXISTS documents;
