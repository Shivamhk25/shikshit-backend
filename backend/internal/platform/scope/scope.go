// Package scope carries a caller's record-level limits from the access check to the services, for resources whose
// reads are narrowed to the records the caller is assigned to (a teacher's class sections).
package scope

import "context"

type classSectionsKey struct{}

// WithClassSections limits reads to the given class-section mappings (class_section_mappings.id). An empty list
// still limits: it means "assigned to nothing", so nothing is visible.
func WithClassSections(ctx context.Context, ids []string) context.Context {
	if ids == nil {
		ids = []string{}
	}
	return context.WithValue(ctx, classSectionsKey{}, ids)
}

// ClassSections returns the class-section limit and whether one applies. No limit means the caller sees the whole
// institute (or the call did not come through the API, as with the import engine).
func ClassSections(ctx context.Context) ([]string, bool) {
	ids, ok := ctx.Value(classSectionsKey{}).([]string)
	return ids, ok
}
