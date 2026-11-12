-- Sends one JSON notification on the `users_changes` channel for every row
-- change on the `users` table. The payload intentionally includes the full
-- row so the listener does not have to query Postgres again.
--
-- Keep the payload under Postgres' ~8000 byte NOTIFY limit. For wide rows,
-- send only the id (and maybe changed columns) and let the listener fetch
-- the row by id instead.

-- define the trigger function to send notifications on row changes
CREATE OR REPLACE FUNCTION notify_users_changes()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    payload  jsonb;
    row_data jsonb;
    old_data jsonb;
    row_id   bigint;
BEGIN
    IF TG_OP = 'DELETE' THEN
        row_data := to_jsonb(OLD);
        old_data := to_jsonb(OLD);
    ELSIF TG_OP = 'UPDATE' THEN
        row_data := to_jsonb(NEW);
        old_data := to_jsonb(OLD);
    ELSE
        row_data := to_jsonb(NEW);
    END IF;

    row_id := (row_data ->> 'id')::bigint; --assuming all rows id are bigint convertible

    payload := jsonb_build_object(
        'action', TG_OP,
        'table', TG_TABLE_NAME,
        'id', row_id,
        'data', row_data,
        'timestamp', clock_timestamp()
    );

    -- Include the previous row only for updates and deletes.
    IF old_data IS NOT NULL THEN
        payload := payload || jsonb_build_object('old', old_data);
    END IF;

    PERFORM pg_notify('users_changes', payload::text);

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;

    RETURN NEW;
END;
$$;

-- create the trigger to call the function on row changes or bind the trigger to the users table
CREATE TRIGGER users_notify_changes
AFTER INSERT OR UPDATE OR DELETE ON users
FOR EACH ROW
EXECUTE FUNCTION notify_users_changes();

---- create above / drop below ----

DROP TRIGGER IF EXISTS users_notify_changes ON users;
DROP FUNCTION IF EXISTS notify_users_changes();
