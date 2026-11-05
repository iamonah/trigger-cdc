-- Logical replication setup for the pglogrepl experiment.
--
-- The publication tells Postgres which tables to stream. REPLICA IDENTITY FULL
-- makes UPDATE/DELETE messages include the complete previous row instead of
-- only the primary key.
CREATE PUBLICATION users_publication FOR TABLE users;

ALTER TABLE users REPLICA IDENTITY FULL;

---- create above / drop below ----

ALTER TABLE users REPLICA IDENTITY DEFAULT;

DROP PUBLICATION IF EXISTS users_publication;
