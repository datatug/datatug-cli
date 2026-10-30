package faithful

import "testing"

// featureFixture builds a SQLite database that uses every feature the copy carries or reports.
func featureFixture(t testing.TB) Endpoint {
	e := newSQLite(t)
	execAll(t, e.DB,
		`CREATE TABLE "Parent" (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name VARCHAR(20) NOT NULL,
			code CHAR(3) DEFAULT 'ABC',
			born DATE,
			seen DATETIME,
			odd DATETIME,
			price DECIMAL(10,2) NOT NULL DEFAULT 0,
			exact DECIMAL_TEXT(10,2),
			ok BOOLEAN DEFAULT 1,
			photo BLOB,
			small INT2,
			ratio REAL,
			note TEXT,
			loose,
			doc JSON_TEXT,
			created TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			expr INTEGER DEFAULT (1+2),
			UNIQUE (name, code),
			CONSTRAINT chk_price CHECK (price >= 0),
			CHECK (code GLOB '[A-Z][A-Z][A-Z]')
		)`,
		`CREATE INDEX parent_born ON "Parent" (born DESC, name)`,
		`CREATE UNIQUE INDEX parent_loose ON "Parent" (note) WHERE note IS NOT NULL`,
		`CREATE INDEX parent_lower ON "Parent" (lower(name))`,
		`CREATE TABLE child (
			parent_id INTEGER NOT NULL,
			seq INTEGER NOT NULL,
			label TEXT,
			PRIMARY KEY (parent_id, seq),
			CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES "Parent"(id) ON DELETE CASCADE ON UPDATE NO ACTION
		)`,
		`CREATE TABLE grandchild (
			a INTEGER, b INTEGER, n INTEGER PRIMARY KEY,
			FOREIGN KEY (a, b) REFERENCES child(parent_id, seq) ON DELETE SET NULL ON UPDATE CASCADE
		)`,
		`CREATE TABLE keyless (x TEXT, y INTEGER)`,
		`CREATE VIEW parent_names AS SELECT name FROM "Parent"`,
		`CREATE TRIGGER parent_t AFTER INSERT ON "Parent" BEGIN SELECT 1; END`,
		`INSERT INTO "Parent" (id, name, code, born, seen, odd, price, exact, ok, photo, small, ratio, note, loose, doc)
		 VALUES (1, 'one', 'ABC', '1968-12-08', '2020-01-02 03:04:05', '2020-01-02', 1.98, '12.50', 1, X'00FF10', 7, 0.5, 'n1', 5, '{"a": 1}'),
		        (2, 'two', 'DEF', NULL, '2020-01-02 03:04:05.25', '2020-01-02 03:04', 3, '0.05', 0, NULL, NULL, NULL, NULL, 'text', '[1,2]')`,
		`DELETE FROM sqlite_sequence`, `INSERT INTO sqlite_sequence (name, seq) VALUES ('Parent', 10)`,
		`INSERT INTO child VALUES (1, 1, 'a'), (1, 2, 'b'), (2, 1, 'c')`,
		`INSERT INTO grandchild VALUES (1, 1, 1), (2, 1, 2), (NULL, NULL, 3)`,
		`INSERT INTO keyless VALUES ('a', 1), ('a', 1), (NULL, NULL)`,
	)
	return e
}
