package apiv1

import (
	"github.com/pie360/backend/internal/api/gen"
	"github.com/pie360/backend/internal/platform/crud"
)

// listQuery turns the shared list parameters into a validated query. ParseList treats 0 as "not given", so a client
// that sends page=0 or pageSize=0 explicitly is mapped to an invalid value and rejected.
// The returned errors are nil when the query is valid.
func listQuery(page *gen.Page, size *gen.PageSize, sort *string, q *gen.Search, sorts crud.Sorts, def string) (crud.ListQuery, crud.FieldErrors) {
	var p, s int
	if page != nil {
		if p = int(*page); p == 0 {
			p = -1
		}
	}
	if size != nil {
		if s = int(*size); s == 0 {
			s = -1
		}
	}
	srt, search := "", ""
	if sort != nil {
		srt = *sort
	}
	if q != nil {
		search = string(*q)
	}
	lq, err := crud.ParseList(p, s, srt, search, sorts, def)
	if err != nil {
		return lq, err.(crud.FieldErrors)
	}
	return lq, nil
}

// oneOf returns a field error when v is non-empty and not one of the allowed values.
func oneOf(errs *crud.FieldErrors, field, v string, allowed ...string) {
	if v == "" {
		return
	}
	for _, a := range allowed {
		if v == a {
			return
		}
	}
	errs.Add(field, "invalid_choice", "Choose one of: "+join(allowed)+".")
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
