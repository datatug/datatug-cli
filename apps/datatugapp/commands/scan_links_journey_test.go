package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A scan writes only plain files in plain folders of the project: it does not write through a
// link. These are the journeys of it, through the real command, for the SQLite scan and for the
// PostgreSQL scan (with a fake reader): every file a scan writes, and every folder above one, is
// stood in for by a link that leads outside the project, to a live place or to nothing, in a folder
// that has none of the project yet and in one that has all of it; the scan is refused, names the
// path of the link inside the project and not where it leads, and the tree outside is
// byte-identical afterwards, with nothing new in it. A scan into a clean folder writes what the
// scan of main wrote, byte for byte.

// scanDriverJourney is one driver of the scan, and how to scan it into a project folder.
type scanDriverJourney struct {
	name string
	// prepare makes what the scan reads, outside the project folder, in base, and returns the
	// arguments of the scan into a project folder.
	prepare func(t *testing.T, base string) func(projectDir string) []string
}

var scanDriverJourneys = []scanDriverJourney{
	{"sqlite3", func(t *testing.T, base string) func(string) []string {
		db := filepath.Join(base, "db", "shop.db")
		writeJourneyDB(t, db)
		return func(projectDir string) []string {
			return []string{"-d", projectDir, "-D", "sqlite3", "--path", db, "--db", "shop", "--env", "local"}
		}
	}},
	{"postgres", func(t *testing.T, _ string) func(string) []string {
		usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return newJourneyPgDatabase() })
		return func(projectDir string) []string { return pgScanArgs(projectDir, journeyPgVar, "shop", "local") }
	}},
}

// entriesOfAScan is every file and every folder, below the project folder, that a scan into an
// empty folder writes, slash separated.
func entriesOfAScan(t *testing.T, driver scanDriverJourney) (files, folders []string) {
	t.Helper()
	base := t.TempDir()
	projectDir := filepath.Join(base, "shop-project")
	_, err := runScanCommand(t, driver.prepare(t, base)(projectDir)...)
	require.NoError(t, err)
	for path := range treeWithLinks(t, projectDir) {
		if path == "." {
			continue
		}
		info, statErr := os.Lstat(filepath.Join(projectDir, filepath.FromSlash(path)))
		require.NoError(t, statErr)
		if info.IsDir() {
			folders = append(folders, path)
		} else {
			files = append(files, path)
		}
	}
	slices.Sort(files)
	slices.Sort(folders)
	return files, folders
}

func TestScanJourneyWritesNothingThroughALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("making a symbolic link needs a privilege on Windows; the refusal there is covered with a faked Lstat in internal/plainfs")
	}
	const sentinel = "a file that is not the project's, and that a scan does not write\n"
	for _, driver := range scanDriverJourneys {
		files, folders := entriesOfAScan(t, driver)
		require.NotEmpty(t, files)
		require.NotEmpty(t, folders)
		for _, scanned := range []string{"the first scan of a folder", "a rescan"} {
			for _, entry := range append(slices.Clone(files), folders...) {
				for _, kind := range []string{"a link to a place outside", "a link to nothing"} {
					t.Run(driver.name+", "+scanned+", "+entry+", "+kind, func(t *testing.T) {
						base := t.TempDir()
						projectDir := filepath.Join(base, "shop-project")
						args := driver.prepare(t, base)(projectDir)
						if scanned == "a rescan" {
							_, err := runScanCommand(t, args...)
							require.NoError(t, err)
							require.NoError(t, os.RemoveAll(filepath.Join(projectDir, filepath.FromSlash(entry))))
						}
						outside := filepath.Join(t.TempDir(), "outside")
						require.NoError(t, os.MkdirAll(outside, 0o755))
						target := filepath.Join(outside, "nothing")
						if kind == "a link to a place outside" {
							if slices.Contains(folders, entry) {
								target = outside
								// What a scan writes below the folder is outside too, with other content.
								for _, file := range files {
									if rel, below := strings.CutPrefix(file, entry+"/"); below {
										require.NoError(t, os.MkdirAll(filepath.Join(outside, filepath.Dir(filepath.FromSlash(rel))), 0o755))
										require.NoError(t, os.WriteFile(filepath.Join(outside, filepath.FromSlash(rel)), []byte(sentinel), 0o644))
									}
								}
							} else {
								target = filepath.Join(outside, "file")
								content := sentinel
								if entry == "datatug-project.json" {
									// The scan reads the project file before it writes anything, and a file it
									// cannot read as a project is refused for that: this one it can read.
									content = "{\"created\": {\"at\": \"2001-02-03T04:05:06Z\"}, \"id\": \"shop-project\", \"access\": \"private\"}\n"
								}
								require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
							}
						}
						linkPath := filepath.Join(projectDir, filepath.FromSlash(entry))
						require.NoError(t, os.MkdirAll(filepath.Dir(linkPath), 0o755))
						require.NoError(t, os.Symlink(target, linkPath))
						outsideBefore := treeWithLinks(t, outside)

						_, err := runScanCommand(t, args...)

						require.Error(t, err, "a scan that would write through a link is refused")
						assert.ErrorContains(t, err, entry, "and names the path of the link inside the project")
						assert.NotContains(t, err.Error(), outside, "and never where it leads")
						assert.Equal(t, outsideBefore, treeWithLinks(t, outside), "the tree outside is byte-identical and nothing new is in it")
					})
				}
			}
		}
	}
}

// mainScanHashes is the SHA-256 of every file that the scan of main wrote into an empty folder
// named shop-project, for the databases of the journeys (see scanDriverJourneys; the SQLite file
// is kept in the project, in data/), by driver and path. Main is
// 515632d (fix: name PostgreSQL scan open failures; run the scan against a real server in CI), with
// datatug-core v0.42.3. The project file, whose content holds the time it was made, is checked
// beside them.
var mainScanHashes = map[string]map[string]string{
	"sqlite3": {
		"README.md": "b96bb0e7ff7dda4db9b73e959a75b0414c8f0874fe555a547781c7ea843e4c49",
		"dbmodels/shop/main/tables/Customer/main.Customer.columns.json":            "4712fef74daf8d6a393bd9f4d52ccae4d9252067bcc75266f8f4dad763aa7106",
		"dbmodels/shop/main/tables/order_line/main.order_line.columns.json":        "38d435bd3da08216b0a49c7eadd3866b53046c0cb0bb2b54e43d337192bfd9f4",
		"dbmodels/shop/main/views/customer_names/main.customer_names.columns.json": "6a9ac638498a970ff4d46d260771869391f525dafdcce3042334bc580521d49f",
		"dbmodels/shop/shop.dbmodel.json":                                          "18394265b4cab95e1e196eaff2a2ff8cd32794c7f4b2267eac6c5519b9a16990",
		"environments/local/catalogs/shop/shop.db.json":                            "c0e606238bd56bd89785fb3a266a0dcb4e300e0cefef4ad80fc51e842f860d1d",
		"environments/local/local.env.json":                                        "7332f2864ae6b7ed3a5f3fafd0417425225f50bfde68c89f960fd6e177d20fad",
	},
	"postgres": {
		"README.md":                   "b96bb0e7ff7dda4db9b73e959a75b0414c8f0874fe555a547781c7ea843e4c49",
		"connections/local/shop.json": "f20ed0fb4b91a6b8a03abe90c19cccdb7fab312ce46fe87b7ef1ff2fa5fe1107",
		"dbmodels/shop/public/tables/Customer/public.Customer.columns.json":            "a7457a10ec26f043203d24be06df1220b1251a965ff3f8eaa2c778148223f051",
		"dbmodels/shop/public/tables/order_line/public.order_line.columns.json":        "20cc5163686a21546c3199f93f8a711184e93c9c7b3f997e8c3cc464d4feeceb",
		"dbmodels/shop/public/views/customer_names/public.customer_names.columns.json": "7e958f848c7c5d2a64391773ec934adf9e09bbfdbfcae00c834e5232c55d4d70",
		"dbmodels/shop/sales/tables/Invoice/sales.Invoice.columns.json":                "60665043ad6be662edd27dc75c95ad75f4c64b65c3d608f8e2496764ccd762ed",
		"dbmodels/shop/sales/views/OpenInvoices/sales.OpenInvoices.columns.json":       "2090559f1501c3dae82b62a6651d6ac4cc6a0d41f1be40496ccafc26f8ca9786",
		"dbmodels/shop/shop.dbmodel.json":                                              "18394265b4cab95e1e196eaff2a2ff8cd32794c7f4b2267eac6c5519b9a16990",
		"environments/local/catalogs/shop/shop.db.json":                                "c207b0a0fff521014195a671e7f29ab7a879cd0033a8c8b4389ac22e35250a5e",
		"environments/local/local.env.json":                                            "ad7081589f8210463ceaa43070ef1aad91517d16c411545c06055da583a6a18f",
	},
}

// mainProjectFile is the project file of main, with the time it was made in place of its value.
const mainProjectFile = "{\n\t\"created\": {\n\t\t\"at\": \"<time>\"\n\t},\n\t\"id\": \"shop-project\",\n\t\"access\": \"private\"\n}\n"

func TestScanJourneyIntoACleanFolderWritesWhatMainWrote(t *testing.T) {
	for _, driver := range scanDriverJourneys {
		t.Run(driver.name, func(t *testing.T) {
			base := t.TempDir()
			projectDir := filepath.Join(base, "shop-project")
			skip := ""
			args := driver.prepare(t, base)(projectDir)
			if driver.name == "sqlite3" {
				// The database is in the project, as a first user keeps it: its path is then written
				// relative to the project, the same on every machine.
				db := filepath.Join(projectDir, "data", "shop.db")
				writeJourneyDB(t, db)
				args = []string{"-d", projectDir, "-D", "sqlite3", "--path", db, "--db", "shop", "--env", "local"}
				skip = "data/"
			}

			_, err := runScanCommand(t, args...)
			require.NoError(t, err)

			hashes := treeHashes(t, projectDir, skip)
			_, ok := hashes["datatug-project.json"]
			require.True(t, ok, "the project file is written")
			delete(hashes, "datatug-project.json")
			assert.Equal(t, mainScanHashes[driver.name], hashes, "every file of the scan has the content hash of the scan of main, and there is no other")

			content, err := os.ReadFile(filepath.Join(projectDir, "datatug-project.json"))
			require.NoError(t, err)
			assert.Equal(t, mainProjectFile, regexp.MustCompile(`"at": "[^"]+"`).ReplaceAllString(string(content), `"at": "<time>"`))
		})
	}
}
