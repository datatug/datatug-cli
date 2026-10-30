-- The edge-case fixture of the SQL Server script export (a copy of mysql_edge.sql, changed where the engines differ): names, types, values and keys that a script must get right.
-- Identifiers MySQL can store (kept byte for byte) and ones it cannot (renamed, with a counted warning).
CREATE TABLE "Order Details" ("Order ID" INTEGER NOT NULL, "Product ID" INTEGER NOT NULL, "Unit Price" NUMERIC(10,2) NOT NULL DEFAULT 0, Quantity INTEGER NOT NULL DEFAULT 1, PRIMARY KEY ("Order ID", "Product ID"));
CREATE TABLE "Production.Product" (ProductID INTEGER PRIMARY KEY AUTOINCREMENT, "Name.Full" TEXT NOT NULL, Color TEXT DEFAULT 'red', ModifiedDate TEXT NOT NULL DEFAULT '2025-11-14 12:13:16.797', CONSTRAINT "UQ_Production.Product_Name.Full" UNIQUE ("Name.Full"));
CREATE INDEX "Production.Product.IX_Color" ON "Production.Product" (Color DESC, "Name.Full");
CREATE TABLE "back`tick" ("co`l" INTEGER PRIMARY KEY, "a""b" TEXT, "x y" TEXT);
CREATE TABLE "trailing " (id INTEGER PRIMARY KEY, "c " TEXT, "  lead" TEXT);
CREATE TABLE "line
break" (id INTEGER PRIMARY KEY, "co
l" TEXT, "tab	col" TEXT);
CREATE TABLE "" (id INTEGER PRIMARY KEY, "" TEXT);
CREATE TABLE "Ünï_t" (id INTEGER PRIMARY KEY, "Case" TEXT);
CREATE TABLE "ünï_t" (id INTEGER PRIMARY KEY, "case" TEXT);
CREATE TABLE "select" ("order" INTEGER PRIMARY KEY, "group" TEXT, "from" TEXT, "Comment" TEXT, "key" TEXT, "primary" TEXT);
CREATE TABLE "ünïcode_日本語_😀" ("Ünï" TEXT, "ünï" TEXT, "ñ" INTEGER PRIMARY KEY, "😀" TEXT);
CREATE TABLE "a-very-long-table-name-that-is-more-than-sixty-four-characters-long-for-sure" ("a-very-long-column-name-that-is-more-than-sixty-four-characters-long-for-sure" INTEGER PRIMARY KEY, v TEXT);
CREATE INDEX "an-index-name-that-is-more-than-sixty-four-characters-long-for-sure-yes" ON "a-very-long-table-name-that-is-more-than-sixty-four-characters-long-for-sure" (v);
CREATE TABLE "back\slash/and;--/*x*/'q'" ("c\" INTEGER PRIMARY KEY, "d;" TEXT);
CREATE TABLE ix_names (id INTEGER PRIMARY KEY, a TEXT, b TEXT);
CREATE INDEX "ix " ON ix_names (a);
CREATE INDEX "PRIMARY" ON ix_names (b);
CREATE INDEX "IX_É" ON ix_names (a, b);
CREATE INDEX "ix_é" ON ix_names (b, a);

-- Types and values.
CREATE TABLE types_edge (
  id INTEGER PRIMARY KEY,
  i_big INTEGER, i2 INT2, i4 INT4, flag BOOLEAN,
  r REAL, f4 FLOAT4, d_fixed DECIMAL(10,2), d_text DECIMAL_TEXT(12,4), d_bare NUMERIC, d_money MONEY,
  dt DATETIME, dt_frac DATETIME, d DATE, t TIME, dt_dateonly DATETIME, tz TIMESTAMPTZ,
  c CHAR(5), vc VARCHAR(10), nv NVARCHAR(40), tx TEXT, long_tx TEXT, js JSON_TEXT,
  b BLOB, big_b BLOB, mixed, untyped
);
INSERT INTO types_edge (id, i_big, i2, i4, flag, r, f4, d_fixed, d_text, d_bare, d_money, dt, dt_frac, d, t, dt_dateonly, tz, c, vc, nv, tx, long_tx, js, b, big_b, mixed, untyped) VALUES
 (1, 9223372036854775807, -32768, 2147483647, 1, 0.1, 1.5, 12.50, '.0000', 5, 3.5, '2024-02-29 23:59:59', '2024-02-29 23:59:59.123456', '2024-02-29', '23:59:59', '2016-07-04', '2024-01-01 00:00:00+00', 'ab', 'x', 'Ünïcode 日本語 😀', 'plain', 'long', '{"a": 1}', x'00ff10', zeroblob(46000), 1, 'a'),
 (2, -9223372036854775808, 32767, -2147483648, 0, -1.7976931348623157e308, 3.4, -0.01, '-0.00', 1.25, 7, '1000-01-01 00:00:00', '2024-02-29 23:59:59.5', '9999-12-31', '00:00:00', '2016-07-04', '2024-01-01 00:00:00+00', 'abcde', 'abcdefghij', '', '', '', '[]', x'', x'', 'text', 3.5),
 (3, 0, 0, 0, NULL, 5e-324, NULL, 0, '1.5', 0.5, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, x'ff', x'00');
UPDATE types_edge SET long_tx = (SELECT group_concat('line ' || value || char(10) || char(13) || 'GO' || char(10), '') FROM (WITH RECURSIVE s(value) AS (SELECT 1 UNION ALL SELECT value+1 FROM s WHERE value < 3000) SELECT value FROM s)) WHERE id = 1;
UPDATE types_edge SET big_b = hex(zeroblob(100)) WHERE id = 3;

-- Text that is not plain: every row must come back byte for byte.
CREATE TABLE hostile (id INTEGER PRIMARY KEY, v TEXT, w TEXT NOT NULL DEFAULT '');
INSERT INTO hostile (id, v, w) VALUES
 (1, 'it''s', 'quote'),
 (2, 'back\slash', 'backslash'),
 (3, 'trailing backslash\', 'trailing'),
 (4, char(10) || 'GO', 'newline then GO'),
 (5, char(10) || '\! echo pwned', 'newline then client command'),
 (6, char(10) || 'source /etc/passwd', 'newline then source'),
 (7, char(10) || '/', 'newline then slash'),
 (8, char(13) || char(10) || 'DROP DATABASE x;', 'CR LF'),
 (9, '; DROP TABLE hostile; --', 'semicolon'),
 (10, '-- comment', 'comment'),
 (11, '/* c */ /*!50000 SELECT 1 */', 'block comment'),
 (12, '$(touch /tmp/x) `id` ${HOME} &', 'shell'),
 (13, 'tab' || char(9) || 'x', 'tab'),
 (14, char(8232) || 'ls' || char(8233) || 'ls' || char(133), 'unicode separators'),
 (15, '😀 𝄞 日本語 Ünï', 'non-BMP'),
 (16, '  leading and trailing  ', 'spaces'),
 (17, 'a' || char(26) || 'b' || char(127) || char(1), 'control characters'),
 (18, '%s %d \n \x \0 \q', 'format'),
 (19, '', 'empty'),
 (20, NULL, 'null'),
 (22, 'ok', 'plain');
-- A NUL byte or text that is not UTF-8 cannot be an NVARCHAR text: the column is carried as bytes.
CREATE TABLE hostile_nul (id INTEGER PRIMARY KEY, v TEXT);
INSERT INTO hostile_nul VALUES (1, 'x' || char(0) || 'y'), (2, char(10) || 'GO'), (3, 'plain');
CREATE TABLE hostile_utf8 (id INTEGER PRIMARY KEY, v TEXT);
INSERT INTO hostile_utf8 VALUES (1, CAST(x'c328ff' AS TEXT)), (2, char(10) || 'GO'), (3, 'plain');
CREATE TABLE binary_edge (id INTEGER PRIMARY KEY, b BLOB, b2 BLOB);
INSERT INTO binary_edge VALUES (1, x'', NULL), (2, x'00', x'0a0d5c27'), (3, x'ff', x'0a474f0a');

-- Keys: text foreign keys of differing declared types, widths, composites, self references, cascade.
CREATE TABLE parent_txt (code VARCHAR(5) PRIMARY KEY, name TEXT);
CREATE TABLE child_txt (id INTEGER PRIMARY KEY, code TEXT REFERENCES parent_txt(code) ON UPDATE CASCADE ON DELETE SET NULL, big_code NVARCHAR(300));
CREATE TABLE parent_int (id INT4 PRIMARY KEY, n TEXT);
CREATE TABLE child_int (id INTEGER PRIMARY KEY, pid INTEGER REFERENCES parent_int(id) ON DELETE CASCADE, pid2 INT2 REFERENCES parent_int(id));
CREATE TABLE child_named (id INTEGER PRIMARY KEY, pid INTEGER, CONSTRAINT fk_child_named FOREIGN KEY (pid) REFERENCES parent_int(id) ON DELETE CASCADE ON UPDATE RESTRICT);
CREATE TABLE tree (id INTEGER PRIMARY KEY, parent INTEGER REFERENCES tree(id) ON DELETE RESTRICT, name TEXT UNIQUE);
CREATE TABLE comp_parent (a INTEGER, b TEXT, PRIMARY KEY (a, b));
CREATE TABLE comp_child (id INTEGER PRIMARY KEY, a INTEGER, b TEXT, FOREIGN KEY (a, b) REFERENCES comp_parent (a, b) DEFERRABLE INITIALLY DEFERRED);
CREATE TABLE fk_missing_parent (id INTEGER PRIMARY KEY, x INTEGER REFERENCES not_a_table(id));
CREATE TABLE fk_nonunique (id INTEGER PRIMARY KEY, v INTEGER);
CREATE TABLE fk_nonunique_child (id INTEGER PRIMARY KEY, v INTEGER REFERENCES fk_nonunique(v));
CREATE TABLE fk_setnull_notnull (id INTEGER PRIMARY KEY, p INTEGER NOT NULL REFERENCES parent_int(id) ON DELETE SET NULL ON UPDATE SET DEFAULT);
CREATE TABLE fk_orphans (id INTEGER PRIMARY KEY, p INTEGER REFERENCES parent_int(id));
CREATE TABLE no_pk (a TEXT, b INTEGER);
CREATE TABLE no_pk_dup (a TEXT, b INTEGER);
CREATE TABLE without_rowid_t (k TEXT PRIMARY KEY, v INTEGER) WITHOUT ROWID;
CREATE TABLE autoinc_gap (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT);
CREATE TABLE zero_id (id INTEGER PRIMARY KEY, v TEXT);
CREATE TABLE checks_t (id INTEGER PRIMARY KEY, g TEXT CHECK (g IN ('M','F')), code TEXT CHECK (code GLOB '[A-Z][A-Z]*'), n INTEGER CONSTRAINT "n_positive" CHECK (n >= 0), s TEXT CHECK (length(s) < 10 AND s <> ''), bad TEXT CHECK (typeof(bad) = 'text'), CHECK (n < 1000 OR g IS NULL));
CREATE TABLE defaults_t (id INTEGER PRIMARY KEY, ts TEXT DEFAULT CURRENT_TIMESTAMP, ts2 DATETIME DEFAULT CURRENT_TIMESTAMP, dd DATE DEFAULT CURRENT_DATE, n INTEGER DEFAULT 5, q TEXT DEFAULT 'it''s', bq TEXT DEFAULT 'back\slash', neg REAL DEFAULT -1.5, bool_d BOOLEAN DEFAULT 1, expr INTEGER DEFAULT (1+2), dec DECIMAL(5,2) DEFAULT 0.10, tdec DECIMAL_TEXT(5,2) DEFAULT '0.00', blobd BLOB DEFAULT x'01');
CREATE TABLE wide_row (id INTEGER PRIMARY KEY, a VARCHAR(16000), b VARCHAR(16000), c VARCHAR(16000), d VARCHAR(16000), e VARCHAR(16000));
CREATE TABLE long_key (a TEXT, b TEXT, c TEXT, PRIMARY KEY (a, b, c));
CREATE TABLE long_index (id INTEGER PRIMARY KEY, a TEXT, b TEXT, c TEXT);
CREATE INDEX long_index_abc ON long_index (a, b, c);
CREATE TABLE partial_ix (id INTEGER PRIMARY KEY, a TEXT);
CREATE INDEX partial_ix_a ON partial_ix (a) WHERE a IS NOT NULL;
CREATE TABLE uniq_t (id INTEGER PRIMARY KEY, a TEXT, b TEXT, UNIQUE (a, b));
CREATE UNIQUE INDEX uniq_t_b ON uniq_t (b DESC);
CREATE TABLE ts_default_frac (id INTEGER PRIMARY KEY, t3 DATETIME);
CREATE TABLE inf_t (id INTEGER PRIMARY KEY, v REAL);
CREATE TABLE 'type_changes' (id INTEGER PRIMARY KEY, small INT2, wide TEXT, decl_char CHAR(3), nodecl, mixed_n NUMERIC(4,1));

INSERT INTO parent_txt VALUES ('A', 'a'), ('B', 'b'), ('abcde', 'long');
INSERT INTO child_txt VALUES (1, 'A', (SELECT substr(group_concat('x',''),1,250) FROM (WITH RECURSIVE s(v) AS (SELECT 1 UNION ALL SELECT v+1 FROM s WHERE v<250) SELECT v FROM s))), (2, NULL, NULL), (3, 'abcde', NULL);
INSERT INTO parent_int VALUES (1, 'one'), (2, 'two'), (70000, 'big');
INSERT INTO child_int VALUES (1, 1, 2), (2, 70000, NULL), (3, NULL, 1);
INSERT INTO child_named VALUES (1, 1);
INSERT INTO tree VALUES (1, NULL, 'root'), (2, 1, 'kid'), (3, 2, NULL), (4, 1, NULL);
INSERT INTO comp_parent VALUES (1, 'a'), (1, 'b');
INSERT INTO comp_child VALUES (1, 1, 'a'), (2, NULL, 'a');
INSERT INTO fk_missing_parent VALUES (1, 5);
INSERT INTO fk_nonunique VALUES (1, 1), (2, 1);
INSERT INTO fk_nonunique_child VALUES (1, 1);
INSERT INTO fk_setnull_notnull VALUES (1, 1);
PRAGMA foreign_keys = OFF;
INSERT INTO fk_orphans VALUES (1, 1), (2, 999);
INSERT INTO no_pk VALUES ('x', 1), ('x', 1), (NULL, NULL), ('y', 2);
INSERT INTO no_pk_dup VALUES ('b', 2), ('a', 1), ('b', 2);
INSERT INTO without_rowid_t VALUES ('k2', 2), ('k1', 1);
INSERT INTO autoinc_gap VALUES (1, 'a'), (2, 'b'), (3, 'c');
DELETE FROM autoinc_gap WHERE id > 1;
INSERT INTO zero_id VALUES (0, 'zero'), (1, 'one'), (-5, 'neg');
INSERT INTO checks_t VALUES (1, 'M', 'AB', 1, 'ok', 'x'), (2, 'F', 'ZZZ', 0, 'abc', 'y'), (3, NULL, 'QQ', 5, 'q', 'z');
INSERT INTO defaults_t (id, ts, ts2, dd) VALUES (1, '2024-01-01 00:00:00', '2024-01-01 00:00:00', '2024-01-01');
INSERT INTO wide_row VALUES (1, 'a', 'b', 'c', 'd', 'e');
INSERT INTO long_key VALUES (printf('%0100d', 1), printf('%0100d', 2), printf('%0100d', 3));
INSERT INTO long_index VALUES (1, printf('%0300d', 1), printf('%0300d', 2), printf('%0300d', 3));
INSERT INTO partial_ix VALUES (1, 'a'), (2, NULL);
INSERT INTO uniq_t VALUES (1, 'a', 'b'), (2, 'a', NULL), (3, NULL, NULL), (4, NULL, NULL);
INSERT INTO ts_default_frac VALUES (1, '2024-01-01 00:00:00.5'), (2, '2024-01-01 00:00:00.123');
INSERT INTO inf_t VALUES (1, 9e999), (2, -9e999), (3, 1.5);
INSERT INTO type_changes VALUES (1, 40000, 'x', 'abcd', 1, 1.25), (2, 5, 'y', 'a', 'two', 2);
INSERT INTO "Order Details" VALUES (1, 1, 9.99, 2), (1, 2, 1.5, 1);
INSERT INTO "Production.Product" ("Name.Full", Color) VALUES ('Widget', 'blue'), ('Gadget', NULL);
INSERT INTO "back`tick" VALUES (1, 'q"uote', 'sp ace');
INSERT INTO "trailing " VALUES (1, 'c', 'lead');
INSERT INTO "line
break" VALUES (1, 'a', 'b');
INSERT INTO "" VALUES (1, 'empty');
INSERT INTO "Ünï_t" VALUES (1, 'U');
INSERT INTO "ünï_t" VALUES (1, 'u');
INSERT INTO "select" VALUES (1, 'g', 'f', 'c', 'k', 'p');
INSERT INTO "ünïcode_日本語_😀" VALUES ('A', 'a', 1, 'smile');
INSERT INTO "a-very-long-table-name-that-is-more-than-sixty-four-characters-long-for-sure" VALUES (1, 'v');
INSERT INTO "back\slash/and;--/*x*/'q'" VALUES (1, 'd');
INSERT INTO ix_names VALUES (1, 'a', 'b');

-- SQL Server only: names, keys and values the engine treats in its own way.
CREATE TABLE "br]acket" ("co]l" INTEGER PRIMARY KEY, "]]" TEXT, "[x" TEXT);
INSERT INTO "br]acket" VALUES (1, 'a', 'b');
CREATE TABLE "dollar$(name)" ("c$(x)" INTEGER PRIMARY KEY, v TEXT);
INSERT INTO "dollar$(name)" VALUES (1, '$(cmd) and ${x} and $ (');
CREATE TABLE "Trailing" (id INTEGER PRIMARY KEY, "x" TEXT, "x " TEXT);
INSERT INTO "Trailing" VALUES (1, 'a', 'b');
CREATE TABLE "only spaces" ("   " INTEGER PRIMARY KEY, " " TEXT);
INSERT INTO "only spaces" VALUES (1, 'x');
CREATE TABLE "name-of-exactly-128-characters-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" (id INTEGER PRIMARY KEY);
CREATE TABLE "name-of-129-characters-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" (id INTEGER PRIMARY KEY);
INSERT INTO "name-of-exactly-128-characters-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" VALUES (1);
INSERT INTO "name-of-129-characters-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" VALUES (1);
-- a constraint name that is a table name, and two constraints with one name
CREATE TABLE named_keys (id INTEGER PRIMARY KEY, a TEXT, b TEXT, CONSTRAINT "tree" UNIQUE (a), CONSTRAINT "shared_name" UNIQUE (b));
CREATE TABLE named_keys2 (id INTEGER PRIMARY KEY, a TEXT, CONSTRAINT "shared_name" UNIQUE (a));
INSERT INTO named_keys VALUES (1, 'a', 'b');
INSERT INTO named_keys2 VALUES (1, 'a');
-- cascade graphs: a diamond, a cycle, a self reference, a second key between two tables
CREATE TABLE cas_p (id INTEGER PRIMARY KEY);
CREATE TABLE cas_a (id INTEGER PRIMARY KEY, p INTEGER REFERENCES cas_p(id) ON DELETE CASCADE ON UPDATE CASCADE);
CREATE TABLE cas_b (id INTEGER PRIMARY KEY, p INTEGER REFERENCES cas_p(id) ON DELETE CASCADE ON UPDATE CASCADE);
CREATE TABLE cas_g (id INTEGER PRIMARY KEY, a INTEGER REFERENCES cas_a(id) ON DELETE CASCADE ON UPDATE SET NULL, b INTEGER REFERENCES cas_b(id) ON DELETE CASCADE ON UPDATE CASCADE);
CREATE TABLE cas_x (id INTEGER PRIMARY KEY, y INTEGER);
CREATE TABLE cas_y (id INTEGER PRIMARY KEY, x INTEGER REFERENCES cas_x(id) ON DELETE CASCADE);
CREATE TABLE cas_self (id INTEGER PRIMARY KEY, up INTEGER REFERENCES cas_self(id) ON DELETE SET NULL ON UPDATE CASCADE);
CREATE TABLE cas_two (id INTEGER PRIMARY KEY, p1 INTEGER REFERENCES cas_p(id) ON DELETE CASCADE, p2 INTEGER REFERENCES cas_p(id) ON DELETE SET NULL ON UPDATE CASCADE);
INSERT INTO cas_p VALUES (1), (2);
INSERT INTO cas_a VALUES (1, 1), (2, 2);
INSERT INTO cas_b VALUES (1, 1), (2, 2);
INSERT INTO cas_g VALUES (1, 1, 1), (2, 2, NULL);
INSERT INTO cas_x VALUES (1, NULL);
INSERT INTO cas_y VALUES (1, 1);
INSERT INTO cas_self VALUES (1, NULL), (2, 1);
INSERT INTO cas_two VALUES (1, 1, 2);
-- text keys: one whose parent is declared with a width, a composite of texts, non-BMP text in a key, a trailing space in a key
CREATE TABLE key_text (code TEXT PRIMARY KEY, v TEXT);
INSERT INTO key_text VALUES ('plain', 'x'), ('𝄞𝄞', 'clef'), ('with space ', 'trail'), ('Ünï', 'accent');
CREATE TABLE key_child (id INTEGER PRIMARY KEY, code VARCHAR(4) REFERENCES key_text(code));
INSERT INTO key_child VALUES (1, 'plain'), (2, '𝄞𝄞');
-- dates at the ends of the range, and a float SQL Server would flush
CREATE TABLE range_t (id INTEGER PRIMARY KEY, d DATE, dt DATETIME, t TIME, tiny REAL, n NUMERIC(38,10), big NUMERIC(20,0));
INSERT INTO range_t VALUES (1, '0001-01-01', '0001-01-01 00:00:00', '00:00:00', 2.2250738585072014e-308, 12345678901234567890123456.0123456789, 12345678901234567890), (2, '9999-12-31', '9999-12-31 23:59:59.999999', '23:59:59.999999', 1e300, -0.0000000001, -1);
-- a unique key over nullable columns that holds one NULL (a plain UNIQUE) and one that holds many (filtered), and a referenced one
CREATE TABLE uniq_one_null (id INTEGER PRIMARY KEY, a TEXT UNIQUE, b INTEGER);
INSERT INTO uniq_one_null VALUES (1, NULL, 1), (2, 'x', 2);
CREATE TABLE uniq_ref (id INTEGER PRIMARY KEY, a TEXT REFERENCES uniq_one_null(a));
INSERT INTO uniq_ref VALUES (1, 'x'), (2, NULL);
CREATE TABLE uniq_many_null (id INTEGER PRIMARY KEY, a TEXT, b INTEGER, c TEXT, UNIQUE (a, b));
CREATE UNIQUE INDEX uniq_many_null_c ON uniq_many_null (c);
INSERT INTO uniq_many_null VALUES (1, NULL, 1, NULL), (2, NULL, 1, NULL), (3, 'a', NULL, 'q'), (4, 'a', NULL, 'r');
CREATE TABLE uniq_ref_many (id INTEGER PRIMARY KEY, c TEXT REFERENCES uniq_many_null(c));
INSERT INTO uniq_ref_many VALUES (1, 'q');
-- Foreign key names share the namespace of constraint names with CHECK names (SQL Server Msg 2714 / 1750): a CHECK named like a foreign key, like a table or like a unique key loses its name; the foreign key keeps its own.
CREATE TABLE fkns_p (id INTEGER PRIMARY KEY, n INTEGER CONSTRAINT "FKNS_DUP" CHECK (n > 0), m INTEGER CONSTRAINT "FKNS_C" CHECK (m > 0));
CREATE TABLE fkns_c (id INTEGER PRIMARY KEY, pid INTEGER, CONSTRAINT fkns_dup FOREIGN KEY (pid) REFERENCES fkns_p(id));
INSERT INTO fkns_p VALUES (1, 1, 1);
INSERT INTO fkns_c VALUES (1, 1);
