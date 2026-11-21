// Package importreg joins the importable entities and setup packs of every module into the one registry the import
// engine serves.
package importreg

import (
	acaimporters "github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/importers"
	stuimporters "github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/importers"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/importer"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Registry returns every importable entity.
func Registry(db tenancy.Beginner) *importer.Registry {
	return importer.NewRegistry(append(acaimporters.Registry(db).All(), stuimporters.Entities(db)...)...)
}

// Packs returns every setup pack, by key.
func Packs() map[string]importer.Pack {
	out := acaimporters.Packs()
	p := stuimporters.Pack()
	out[p.Key] = p
	return out
}
