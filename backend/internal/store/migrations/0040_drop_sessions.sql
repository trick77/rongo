-- Sessions are kit's signed cookies now: the claims travel in the cookie,
-- nothing is stored, and nothing revokes one but rotating
-- BACKEND_SESSION_SECRET. The table and its indexes go; users stays, it is
-- what threads hang off.
DROP TABLE sessions;
