-- Fixture for pkg/secureread's sqlite:// tests. Loaded and executed
-- statement-by-statement at test time (see fixtures_test.go); never checked
-- in as a binary .db file. Shape mirrors
-- apps/datatugapp/commands/cmd_query_test.go's inGitDB fixture (same ids,
-- names and values) plus a `country` column, so the same policy documents
-- and expectations can be exercised against both backends.

CREATE TABLE customers (
    id TEXT PRIMARY KEY,
    name TEXT,
    email TEXT,
    passwordHash TEXT,
    ownerID TEXT,
    country TEXT
);

CREATE TABLE products (
    id TEXT PRIMARY KEY,
    name TEXT,
    price INTEGER
);

INSERT INTO customers (id, name, email, passwordHash, ownerID, country) VALUES ('c1', 'Ann', 'ann@example.com', 'h1', 'alice', 'Canada');
INSERT INTO customers (id, name, email, passwordHash, ownerID, country) VALUES ('c2', 'Ben', 'ben@example.com', 'h2', 'alice', 'Canada');
INSERT INTO customers (id, name, email, passwordHash, ownerID, country) VALUES ('c3', 'Cid', 'cid@example.com', 'h3', 'bob', 'Brazil');

INSERT INTO products (id, name, price) VALUES ('p1', 'pen', 5);
INSERT INTO products (id, name, price) VALUES ('p2', 'book', 10);
INSERT INTO products (id, name, price) VALUES ('p3', 'lamp', 1000000);
