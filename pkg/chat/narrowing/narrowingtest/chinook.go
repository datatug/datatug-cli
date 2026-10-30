// Package narrowingtest holds the fixtures the narrowing tests share: the
// Chinook schema (11 tables) and a fake decision engine standing in for Jev
// (Scorer). It makes no network call and reads no clock.
package narrowingtest

import (
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat/narrowing"
)

// chinookTables is Chinook's table list: name and "column:type" pairs, read from
// the sample database shipped in pkg/dbcopy/testdata/chinook.db.
var chinookTables = []struct {
	name    string
	columns string
}{
	{"Album", "AlbumId:INTEGER, Title:NVARCHAR(160), ArtistId:INTEGER"},
	{"Artist", "ArtistId:INTEGER, Name:NVARCHAR(120)"},
	{"Customer", "CustomerId:INTEGER, FirstName:NVARCHAR(40), LastName:NVARCHAR(20), Company:NVARCHAR(80), Address:NVARCHAR(70), City:NVARCHAR(40), State:NVARCHAR(40), Country:NVARCHAR(40), PostalCode:NVARCHAR(10), Phone:NVARCHAR(24), Fax:NVARCHAR(24), Email:NVARCHAR(60), SupportRepId:INTEGER"},
	{"Employee", "EmployeeId:INTEGER, LastName:NVARCHAR(20), FirstName:NVARCHAR(20), Title:NVARCHAR(30), ReportsTo:INTEGER, BirthDate:DATETIME, HireDate:DATETIME, Address:NVARCHAR(70), City:NVARCHAR(40), State:NVARCHAR(40), Country:NVARCHAR(40), PostalCode:NVARCHAR(10), Phone:NVARCHAR(24), Fax:NVARCHAR(24), Email:NVARCHAR(60)"},
	{"Genre", "GenreId:INTEGER, Name:NVARCHAR(120)"},
	{"Invoice", "InvoiceId:INTEGER, CustomerId:INTEGER, InvoiceDate:DATETIME, BillingAddress:NVARCHAR(70), BillingCity:NVARCHAR(40), BillingState:NVARCHAR(40), BillingCountry:NVARCHAR(40), BillingPostalCode:NVARCHAR(10), Total:NUMERIC(10,2)"},
	{"InvoiceLine", "InvoiceLineId:INTEGER, InvoiceId:INTEGER, TrackId:INTEGER, UnitPrice:NUMERIC(10,2), Quantity:INTEGER"},
	{"MediaType", "MediaTypeId:INTEGER, Name:NVARCHAR(120)"},
	{"Playlist", "PlaylistId:INTEGER, Name:NVARCHAR(120)"},
	{"PlaylistTrack", "PlaylistId:INTEGER, TrackId:INTEGER"},
	{"Track", "TrackId:INTEGER, Name:NVARCHAR(200), AlbumId:INTEGER, MediaTypeId:INTEGER, GenreId:INTEGER, Composer:NVARCHAR(220), Milliseconds:INTEGER, Bytes:INTEGER, UnitPrice:NUMERIC(10,2)"},
}

// Chinook returns the 11 relations of the Chinook sample database, as the chat's
// stored schema lists them.
func Chinook() []api.CatalogRelation {
	relations := make([]api.CatalogRelation, 0, len(chinookTables))
	for _, table := range chinookTables {
		relation := api.CatalogRelation{Schema: "main", Name: table.name, DbType: "TABLE"}
		for _, column := range strings.Split(table.columns, ", ") {
			name, dbType, _ := strings.Cut(column, ":")
			relation.Columns = append(relation.Columns, api.CatalogColumn{Name: name, DbType: dbType})
		}
		relations = append(relations, relation)
	}
	return relations
}

// ChinookLinks returns Chinook's foreign keys.
func ChinookLinks() []narrowing.Link {
	pairs := [][2]string{
		{"Album", "Artist"}, {"Customer", "Employee"}, {"Employee", "Employee"},
		{"Invoice", "Customer"}, {"InvoiceLine", "Invoice"}, {"InvoiceLine", "Track"},
		{"Track", "Album"}, {"Track", "MediaType"}, {"Track", "Genre"},
		{"PlaylistTrack", "Playlist"}, {"PlaylistTrack", "Track"},
	}
	links := make([]narrowing.Link, 0, len(pairs))
	for _, pair := range pairs {
		links = append(links, narrowing.Link{FromSchema: "main", From: pair[0], ToSchema: "main", To: pair[1]})
	}
	return links
}
