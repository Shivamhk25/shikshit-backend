#!/usr/bin/env python3
"""Generates the repetitive HTTP layer of master-data resources (FND-10, first version).

For every resource defined in RESOURCES it writes
  api/openapi/modules/<module>-<stem>.yaml   the contract: list / create / get / replace / delete with declared access
  internal/api/v1/<stem>_gen.go              the handlers, which call the resource's service and map errors to problems

What stays handwritten per resource (see years_map.go and friends): converting between the generated API types and
the domain types, and parsing the resource's own list filters. Run from backend/:  python3 scripts/gen_resources.py
Then `make api-gen` (from the repo root) to regenerate the Go and TypeScript clients from the contract.
"""
import pathlib
import subprocess

ROOT = pathlib.Path(__file__).resolve().parent.parent

RESOURCES = [
    dict(
        stem="years", nouns="academic years", module="aca", name="AcademicYear", plural="AcademicYears", path="/academic-years", idParam="yearId",
        perm="aca.academic_years", pkg="years", domain="Year", field="academicYears", noun="academic year", in_use=True,
        sort="code, name, startDate, endDate, createdAt",
        filters=[("status", "string", "enum: [active, inactive]"), ("isCurrent", "boolean", "")],
        entity_required="id, code, name, startDate, endDate, isCurrent, remarks, status, createdAt, updatedAt",
        entity="""
        id: { type: string, format: uuid }
        code: { type: string, maxLength: 20 }
        name: { type: string, maxLength: 100 }
        startDate: { type: string, format: date }
        endDate: { type: string, format: date }
        isCurrent: { type: boolean }
        remarks: { type: string, nullable: true, maxLength: 500 }
        status: { type: string, enum: [active, inactive] }
        createdAt: { type: string, format: date-time }
        updatedAt: { type: string, format: date-time }""",
        input_required="code, name, startDate, endDate",
        input="""
        code: { type: string, maxLength: 20 }
        name: { type: string, maxLength: 100 }
        startDate: { type: string, format: date }
        endDate: { type: string, format: date, description: Must be after the start date and must not overlap another year. }
        isCurrent: { type: boolean, default: false, description: Only one year can be current; it must be active. }
        remarks: { type: string, nullable: true, maxLength: 500 }
        status: { type: string, enum: [active, inactive], default: active }""",
    ),
    dict(
        stem="terms", nouns="academic terms", module="aca", name="AcademicTerm", plural="AcademicTerms", path="/academic-terms", idParam="termId",
        perm="aca.academic_terms", pkg="terms", domain="Term", field="academicTerms", noun="academic term", in_use=False,
        sort="code, title, startDate, endDate, createdAt",
        filters=[("yearId", "string", "format: uuid")],
        entity_required="id, yearId, yearName, code, title, startDate, endDate, days, color, createdAt, updatedAt",
        entity="""
        id: { type: string, format: uuid }
        yearId: { type: string, format: uuid }
        yearName: { type: string }
        code: { type: string, maxLength: 20 }
        title: { type: string, maxLength: 100 }
        startDate: { type: string, format: date }
        endDate: { type: string, format: date }
        days: { type: integer, description: "Number of days, counting both end dates." }
        color: { type: string, nullable: true, pattern: '^#[0-9A-Fa-f]{6}$' }
        createdAt: { type: string, format: date-time }
        updatedAt: { type: string, format: date-time }""",
        input_required="yearId, code, title, startDate, endDate",
        input="""
        yearId: { type: string, format: uuid }
        code: { type: string, maxLength: 20, description: Unique within the year. }
        title: { type: string, maxLength: 100 }
        startDate: { type: string, format: date, description: Inside the academic year. }
        endDate: { type: string, format: date, description: Inside the academic year; terms of one year must not overlap. }
        color: { type: string, nullable: true, pattern: '^#[0-9A-Fa-f]{6}$' }""",
    ),
    dict(
        stem="classes", nouns="classes", module="aca", name="SchoolClass", plural="SchoolClasses", path="/classes", idParam="classId",
        perm="aca.classes", pkg="classes", domain="Class", field="classes", noun="class", in_use=False,
        sort="code, name, displayOrder, youngestAgeMonths, createdAt",
        filters=[("status", "string", "enum: [active, inactive]")],
        entity_required="id, code, name, displayOrder, youngestAgeMonths, oldestAgeMonths, description, status, createdAt, updatedAt",
        entity="""
        id: { type: string, format: uuid }
        code: { type: string, maxLength: 20 }
        name: { type: string, maxLength: 100 }
        displayOrder: { type: integer, minimum: 0, maximum: 100000 }
        youngestAgeMonths: { type: integer, nullable: true, minimum: 0, maximum: 600 }
        oldestAgeMonths: { type: integer, nullable: true, minimum: 0, maximum: 600 }
        description: { type: string, nullable: true, maxLength: 500 }
        status: { type: string, enum: [active, inactive] }
        createdAt: { type: string, format: date-time }
        updatedAt: { type: string, format: date-time }""",
        input_required="code, name",
        input="""
        code: { type: string, maxLength: 20 }
        name: { type: string, maxLength: 100 }
        displayOrder: { type: integer, minimum: 0, maximum: 100000, default: 0 }
        youngestAgeMonths: { type: integer, nullable: true, minimum: 0, maximum: 600 }
        oldestAgeMonths: { type: integer, nullable: true, minimum: 0, maximum: 600, description: Not below the youngest age. }
        description: { type: string, nullable: true, maxLength: 500 }
        status: { type: string, enum: [active, inactive], default: active }""",
    ),
    dict(
        stem="sections", nouns="sections", module="aca", name="Section", plural="Sections", path="/sections", idParam="sectionId",
        perm="aca.sections", pkg="sections", domain="Section", field="sections", noun="section", in_use=False,
        sort="code, name, createdAt",
        filters=[("status", "string", "enum: [active, inactive]")],
        entity_required="id, code, name, shortName, description, status, createdAt, updatedAt",
        entity="""
        id: { type: string, format: uuid }
        code: { type: string, maxLength: 20 }
        name: { type: string, maxLength: 100 }
        shortName: { type: string, nullable: true, maxLength: 20 }
        description: { type: string, nullable: true, maxLength: 500 }
        status: { type: string, enum: [active, inactive] }
        createdAt: { type: string, format: date-time }
        updatedAt: { type: string, format: date-time }""",
        input_required="code, name",
        input="""
        code: { type: string, maxLength: 20 }
        name: { type: string, maxLength: 100 }
        shortName: { type: string, nullable: true, maxLength: 20 }
        description: { type: string, nullable: true, maxLength: 500 }
        status: { type: string, enum: [active, inactive], default: active }""",
    ),
    dict(
        stem="subjects", nouns="subjects", module="aca", name="Subject", plural="Subjects", path="/subjects", idParam="subjectId",
        perm="aca.subjects", pkg="subjects", domain="Subject", field="subjects", noun="subject", in_use=False,
        sort="code, name, category, area, credits, createdAt",
        filters=[("status", "string", "enum: [active, inactive]"), ("category", "string", "")],
        entity_required="id, code, name, shortName, color, category, area, scholastic, coScholastic, nonScholastic, credits, description, status, createdAt, updatedAt",
        entity="""
        id: { type: string, format: uuid }
        code: { type: string, maxLength: 20 }
        name: { type: string, maxLength: 100 }
        shortName: { type: string, nullable: true, maxLength: 20 }
        color: { type: string, nullable: true, pattern: '^#[0-9A-Fa-f]{6}$' }
        category: { type: string, maxLength: 50 }
        area: { type: string, maxLength: 50 }
        scholastic: { type: boolean }
        coScholastic: { type: boolean }
        nonScholastic: { type: boolean }
        credits: { type: number, format: double, minimum: 0, maximum: 99.9 }
        description: { type: string, nullable: true, maxLength: 500 }
        status: { type: string, enum: [active, inactive] }
        createdAt: { type: string, format: date-time }
        updatedAt: { type: string, format: date-time }""",
        input_required="code, name, category, area, credits",
        input="""
        code: { type: string, maxLength: 20 }
        name: { type: string, maxLength: 100 }
        shortName: { type: string, nullable: true, maxLength: 20 }
        color: { type: string, nullable: true, pattern: '^#[0-9A-Fa-f]{6}$' }
        category: { type: string, maxLength: 50, description: Free text until product confirms the list. }
        area: { type: string, maxLength: 50, description: Free text until product confirms the list. }
        scholastic: { type: boolean, default: false }
        coScholastic: { type: boolean, default: false }
        nonScholastic: { type: boolean, default: false }
        credits: { type: number, format: double, minimum: 0, maximum: 99.9, description: Kept to one decimal. }
        description: { type: string, nullable: true, maxLength: 500 }
        status: { type: string, enum: [active, inactive], default: active }""",
    ),
]


def lower_first(s):
    return s[0].lower() + s[1:]


def upper_first(s):
    return s[0].upper() + s[1:]


def contract(r):
    n, pl, perm = r["name"], r["plural"], r["perm"]
    ops = dict(list=f"list{pl}", get=f"get{n}", create=f"create{n}", update=f"update{n}", delete=f"delete{n}")
    common = "../components/common.yaml#/components"
    filt = ""
    for fname, ftype, extra in r["filters"]:
        schema = f"type: {ftype}" + (f", {extra}" if extra else "")
        filt += f"        - name: {fname}\n          in: query\n          schema: {{ {schema} }}\n"

    def errs(codes):
        names = {"400": "BadRequest", "401": "Unauthorized", "403": "Forbidden", "404": "NotFound", "409": "Conflict"}
        return "".join(f"        '{c}':\n          $ref: '{common}/responses/{names[c]}'\n" for c in codes)

    def hdr(op, summary, perm_action, extra=""):
        return (f"      operationId: {ops[op]}\n      summary: {summary}\n      tags: [{r['module']}]\n"
                f"      x-permission: {perm}.{perm_action}\n      x-module: {r['module']}\n      x-parity: Companion\n{extra}")

    body = f"""# Generated by scripts/gen_resources.py from the {n} definition. Do not edit by hand.
paths:
  {r['path']}:
    get:
{hdr('list', f"List {r['nouns']}", 'view', f'''      parameters:
        - $ref: '{common}/parameters/Page'
        - $ref: '{common}/parameters/PageSize'
        - name: sort
          in: query
          description: Comma-separated keys, `-` prefix for descending. Allowed `{r['sort']}`.
          schema: {{ type: string }}
        - $ref: '{common}/parameters/Search'
{filt}''')}      responses:
        '200':
          description: One page.
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/{n}Page'
{errs(['400', '401', '403', '404'])}    post:
{hdr('create', f"Add a {r['noun']}", 'create')}      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/{n}Input'
      responses:
        '201':
          description: Created.
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/{n}'
{errs(['400', '401', '403', '404'])}  {r['path']}/{{{r['idParam']}}}:
    parameters:
      - name: {r['idParam']}
        in: path
        required: true
        schema: {{ type: string, format: uuid }}
    get:
{hdr('get', f"Get a {r['noun']}", 'view')}      responses:
        '200':
          description: The {r['noun']}.
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/{n}'
{errs(['401', '403', '404'])}    put:
{hdr('update', f"Replace a {r['noun']}'s fields", 'edit')}      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/{n}Input'
      responses:
        '200':
          description: Updated.
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/{n}'
{errs(['400', '401', '403', '404'])}    delete:
{hdr('delete', f"Delete a {r['noun']} (soft delete; its code can be reused)", 'delete')}      responses:
        '204':
          description: Deleted.
{errs(['401', '403', '404'] + (['409'] if r['in_use'] else []))}components:
  schemas:
    {n}:
      type: object
      required: [{r['entity_required']}]
      properties:{r['entity']}
    {n}Input:
      type: object
      description: Every field is replaced on update. Blank optional text is stored as null.
      required: [{r['input_required']}]
      properties:{r['input']}
    {n}Page:
      type: object
      required: [items, page]
      properties:
        items:
          type: array
          items: {{ $ref: '#/components/schemas/{n}' }}
        page:
          $ref: '{common}/schemas/PageInfo'
"""
    return body


def handlers(r):
    n, pl, pkg, dom, fld = r["name"], r["plural"], r["pkg"], r["domain"], r["field"]
    ln = lower_first(n)
    idf = upper_first(r["idParam"])
    svc = f"{n}Service"
    bad = lambda op: (f"gen.{op}400ApplicationProblemPlusJSONResponse{{BadRequestApplicationProblemPlusJSONResponse: "
                      f"gen.BadRequestApplicationProblemPlusJSONResponse(%s)}}")
    nf = lambda op: (f"gen.{op}404ApplicationProblemPlusJSONResponse{{NotFoundApplicationProblemPlusJSONResponse: "
                     f"gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, \"not_found\", \"\"))}}")
    L, G, C, U, D = f"List{pl}", f"Get{n}", f"Create{n}", f"Update{n}", f"Delete{n}"
    in_use = ""
    if r["in_use"]:
        in_use = f"""	var inUse resource.InUseError
	if errors.As(err, &inUse) {{
		return gen.{D}409ApplicationProblemPlusJSONResponse{{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(
			problemBody(http.StatusConflict, "in_use", inUse.Message))}}, nil
	}}
"""
    return f'''// Code generated by scripts/gen_resources.py from the {n} definition; DO NOT EDIT.

package apiv1

import (
	"context"
	"errors"
	"net/http"

	"github.com/pie360/backend/internal/api/gen"
	"github.com/pie360/backend/internal/modules/{r['module']}/{pkg}"
	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/resource"
)

var _ = resource.ErrNotFound

// {svc} is what the {r['noun']} operations need ({pkg}.Service satisfies it).
type {svc} interface {{
	List(ctx context.Context, q crud.ListQuery, f {pkg}.Filter) (crud.Page[{pkg}.{dom}], error)
	Get(ctx context.Context, id string) ({pkg}.{dom}, error)
	Create(ctx context.Context, actor audit.Actor, in {pkg}.Input) ({pkg}.{dom}, error)
	Update(ctx context.Context, actor audit.Actor, id string, in {pkg}.Input) ({pkg}.{dom}, error)
	Delete(ctx context.Context, actor audit.Actor, id string) error
}}

// With{pl} enables the {r['path']} operations.
func With{pl}(s {svc}) Option {{ return func(h *Handler) {{ h.{fld} = s }} }}

// {L} implements GET {r['path']}.
func (h *Handler) {L}(ctx context.Context, req gen.{L}RequestObject) (gen.{L}ResponseObject, error) {{
	if h.{fld} == nil {{
		return nil, errNotConfigured
	}}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, req.Params.Sort, req.Params.Q, {pkg}.SortKeys, {pkg}.DefaultSort)
	if qerr != nil {{
		return {bad(L) % "validationProblem(qerr)"}, nil
	}}
	f, ferr := {ln}Filter(req.Params)
	if ferr != nil {{
		return {bad(L) % "validationProblem(ferr)"}, nil
	}}
	p, err := h.{fld}.List(ctx, q, f)
	if err != nil {{
		return nil, err
	}}
	out := gen.{n}Page{{Items: make([]gen.{n}, len(p.Items)), Page: gen.PageInfo{{Number: p.Page, Size: p.Size, Total: p.Total}}}}
	for i, it := range p.Items {{
		out.Items[i] = toGen{n}(it)
	}}
	return gen.{L}200JSONResponse(out), nil
}}

// {G} implements GET {r['path']}/{{{r['idParam']}}}.
func (h *Handler) {G}(ctx context.Context, req gen.{G}RequestObject) (gen.{G}ResponseObject, error) {{
	if h.{fld} == nil {{
		return nil, errNotConfigured
	}}
	it, err := h.{fld}.Get(ctx, req.{idf}.String())
	if errors.Is(err, resource.ErrNotFound) {{
		return {nf(G)}, nil
	}}
	if err != nil {{
		return nil, err
	}}
	return gen.{G}200JSONResponse(toGen{n}(it)), nil
}}

// {C} implements POST {r['path']}.
func (h *Handler) {C}(ctx context.Context, req gen.{C}RequestObject) (gen.{C}ResponseObject, error) {{
	if h.{fld} == nil {{
		return nil, errNotConfigured
	}}
	if req.Body == nil {{
		return {bad(C) % 'problemBody(http.StatusBadRequest, "validation_failed", "A request body is required.")'}, nil
	}}
	it, err := h.{fld}.Create(ctx, actorFrom(ctx), {ln}Input(*req.Body))
	var fe crud.FieldErrors
	if errors.As(err, &fe) {{
		return {bad(C) % "validationProblem(fe)"}, nil
	}}
	if err != nil {{
		return nil, err
	}}
	return gen.{C}201JSONResponse(toGen{n}(it)), nil
}}

// {U} implements PUT {r['path']}/{{{r['idParam']}}}.
func (h *Handler) {U}(ctx context.Context, req gen.{U}RequestObject) (gen.{U}ResponseObject, error) {{
	if h.{fld} == nil {{
		return nil, errNotConfigured
	}}
	if req.Body == nil {{
		return {bad(U) % 'problemBody(http.StatusBadRequest, "validation_failed", "A request body is required.")'}, nil
	}}
	it, err := h.{fld}.Update(ctx, actorFrom(ctx), req.{idf}.String(), {ln}Input(*req.Body))
	var fe crud.FieldErrors
	switch {{
	case errors.As(err, &fe):
		return {bad(U) % "validationProblem(fe)"}, nil
	case errors.Is(err, resource.ErrNotFound):
		return {nf(U)}, nil
	case err != nil:
		return nil, err
	}}
	return gen.{U}200JSONResponse(toGen{n}(it)), nil
}}

// {D} implements DELETE {r['path']}/{{{r['idParam']}}}.
func (h *Handler) {D}(ctx context.Context, req gen.{D}RequestObject) (gen.{D}ResponseObject, error) {{
	if h.{fld} == nil {{
		return nil, errNotConfigured
	}}
	err := h.{fld}.Delete(ctx, actorFrom(ctx), req.{idf}.String())
	if errors.Is(err, resource.ErrNotFound) {{
		return {nf(D)}, nil
	}}
{in_use}	if err != nil {{
		return nil, err
	}}
	return gen.{D}204Response{{}}, nil
}}
'''


def main():
    go_files = []
    for r in RESOURCES:
        (ROOT / "api/openapi/modules" / f"{r['module']}-{r['stem']}.yaml").write_text(contract(r))
        path = ROOT / "internal/api/v1" / f"{r['stem']}_gen.go"
        path.write_text(handlers(r))
        go_files.append(str(path))
    # keep the output gofmt-clean so regenerating never produces noise in diffs
    subprocess.run(["gofmt", "-w", *go_files], check=True)
    print(f"generated {len(RESOURCES)} resources")


if __name__ == "__main__":
    main()
