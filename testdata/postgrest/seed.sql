-- Disposable PostgREST fixture, seeded by PostgreSQL's init hook so every row
-- is external to the adapter and to the CLI under test. PostgREST must never be
-- used to write: the reader role has SELECT and no INSERT.
create schema app;

create role authenticator login password 'authenticator-pass' noinherit;
create role anon nologin;
create role app_reader nologin;
create role noaccess nologin;

grant anon to authenticator;
grant app_reader to authenticator;
grant noaccess to authenticator;

grant usage on schema app to anon, app_reader, noaccess;

-- Duplicate qty values (20 four times) are deliberate: a query ordered by qty
-- only is stable only because the adapter appends the id tie-breaker, and the
-- server's db-max-rows=2 turns the five-row read into several physical pages.
create table app.items (
  id bigint primary key,
  title text not null,
  qty int not null,
  price numeric(30, 2) not null,
  owner text not null,
  secret text not null,
  created_at timestamptz not null
);

alter table app.items enable row level security;
create policy items_reader on app.items for select to app_reader using (true);
grant select on app.items to app_reader;

insert into app.items (id, title, qty, price, owner, secret, created_at) values
  (1, 'alpha',   10, 12.50,                 'alice', 'secret-alpha',   '2024-01-01T10:00:00Z'),
  (2, 'beta',    20, 9007199254740993.00,   'bob',   'secret-beta',    '2024-01-02T10:00:00Z'),
  (3, 'gamma',   20, 3.25,                  'alice', 'secret-gamma',   '2024-01-03T10:00:00Z'),
  (4, 'delta',   20, 1.00,                  'bob',   'secret-delta',   '2024-01-04T10:00:00Z'),
  (5, 'epsilon', 20, 2.00,                  'alice', 'secret-epsilon', '2024-01-05T10:00:00Z'),
  (6, 'zeta',    30, 4.00,                  'bob',   'secret-zeta',    '2024-01-06T10:00:00Z');

-- A second relation proves the schema scan lists what the role can see, and the
-- NULL/empty-string pair proves the adapter keeps them distinct.
create table app.labels (
  id bigint primary key,
  name text
);

alter table app.labels enable row level security;
create policy labels_reader on app.labels for select to app_reader using (true);
grant select on app.labels to app_reader;

insert into app.labels (id, name) values
  (1, ''),
  (2, null),
  (3, 'value');
