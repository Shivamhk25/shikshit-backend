"""Field-list DSL for scripts/gen_resources.py.

A resource written with a `fields` list gets its OpenAPI schemas AND the Go conversions between API and domain types
generated from that one list, so the contract, the handlers and the conversions cannot disagree.

A field is a dict:  n (json name), k (kind), and optionally
    req=True      required on create/replace            ro=True   read-only (only in responses, computed by the service)
    max=N         max length (strings)                   min/max for ints (lo=, hi=)
    default=V     default for an optional input          enum=[...] allowed values (kinds enum)
    desc="..."    description

Kinds and the Go types they imply (API / domain):
    string   string / string        string?  *string / *string      int   int / int        int?   *int / *int
    bool     bool (*bool if optional) / bool                          number float64 / float64
    date     Date / string ("2026-04-01")                              uuid  UUID / string
    enum     named string / string (the API type is *Named if optional)
Every resource also has id, createdAt and updatedAt. A domain field is the json name with the first letter upper-cased,
except names ending in "Id", which end in "ID" (yearId -> YearID).
"""


def up(s):
    return s[0].upper() + s[1:]


def dom(n):
    if n == "id":
        return "ID"
    return up(n[:-2] + "ID") if n.endswith("Id") else up(n)


def _schema(f, response):
    k, n = f["k"], f["n"]
    parts = []
    nullable = k.endswith("?")
    base = k.rstrip("?")
    if base == "string":
        parts.append("type: string")
    elif base == "int":
        parts.append("type: integer")
    elif base == "bool":
        parts.append("type: boolean")
    elif base == "number":
        parts.append("type: number, format: double")
    elif base == "date":
        parts.append("type: string, format: date")
    elif base == "uuid":
        parts.append("type: string, format: uuid")
    elif base == "enum":
        parts.append("type: string, enum: [" + ", ".join(f["enum"]) + "]")
    else:
        raise ValueError(f"unknown kind {k} for {n}")
    if nullable:
        parts.append("nullable: true")
    if "max" in f and base == "string":
        parts.append(f"maxLength: {f['max']}")
    if "lo" in f:
        parts.append(f"minimum: {f['lo']}")
    if "hi" in f:
        parts.append(f"maximum: {f['hi']}")
    if "pattern" in f:
        parts.append(f"pattern: '{f['pattern']}'")
    if not response and "default" in f and not f.get("req"):
        d = f["default"]
        parts.append("default: " + (str(d).lower() if isinstance(d, bool) else str(d)))
    if "desc" in f:
        parts.append('description: "' + f["desc"].replace('"', "'") + '"')
    return f"        {n}: {{ " + ", ".join(parts) + " }"


def schemas(r):
    """Fills r['entity'], r['entity_required'], r['input'], r['input_required'] from r['fields']."""
    fs = r["fields"]
    ent = [{"n": "id", "k": "uuid"}] + fs + [{"n": "createdAt", "k": "string"}, {"n": "updatedAt", "k": "string"}]
    lines = []
    for f in ent:
        if f["n"] in ("createdAt", "updatedAt"):
            lines.append(f"        {f['n']}: {{ type: string, format: date-time }}")
        else:
            lines.append(_schema(f, True))
    r["entity"] = "\n" + "\n".join(lines)
    r["entity_required"] = ", ".join(f["n"] for f in ent)
    inp = [f for f in fs if not f.get("ro")]
    r["input"] = "\n" + "\n".join(_schema(f, False) for f in inp)
    r["input_required"] = ", ".join(f["n"] for f in inp if f.get("req"))
    # list filters as (name, type, extra) for the contract
    flt = []
    for x in r["filters"]:
        if isinstance(x, dict):
            t = {"enum": "string", "bool": "boolean", "uuid": "string", "string": "string"}[x["k"]]
            extra = "enum: [" + ", ".join(x["enum"]) + "]" if x["k"] == "enum" else ("format: uuid" if x["k"] == "uuid" else "")
            flt.append((x["n"], t, extra))
        else:
            flt.append(x)
    r["filters_contract"] = flt


def maps(r):
    """Go source for toGen<Name>, <name>Input and <name>Filter."""
    n, pkg, d = r["name"], r["pkg"], r["domain"]
    ln = n[0].lower() + n[1:]
    fs = r["fields"]
    ent = []
    for f in [{"n": "id", "k": "uuid"}] + fs:
        k, nm, g = f["k"], f["n"], up(f["n"])
        base = k.rstrip("?")
        v = f"d.{dom(nm)}"
        if base == "date":
            e = f"apiDate({v})" if k == "date" else f"apiDatePtr({v})"
        elif base == "uuid":
            e = f"mustUUID({v})" if k == "uuid" else f"uuidPtr({v})"
        elif base == "enum":
            e = f"gen.{n}{g}({v})" if k == "enum" else f"enumPtr[gen.{n}{g}]({v})"
        else:
            e = v
        ent.append(f"\t\t{g}: {e},")
    ent.append("\t\tCreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,")

    inp = []
    for f in fs:
        if f.get("ro"):
            continue
        k, nm, g = f["k"], f["n"], up(f["n"])
        base = k.rstrip("?")
        v, req = f"b.{g}", f.get("req")
        nullable = k.endswith("?")
        if base == "string":
            e = v
        elif base == "int":
            e = v if (req or nullable) else f"intOr({v}, {f.get('default', 0)})"
        elif base == "bool":
            e = v if req else f"boolOr({v}, {str(bool(f.get('default', False))).lower()})"
        elif base == "number":
            e = v
        elif base == "date":
            e = f"domainDate({v})" if k == "date" else f"domainDatePtr({v})"
        elif base == "uuid":
            e = f"{v}.String()" if k == "uuid" else f"uuidStrPtr({v})"
        elif base == "enum":
            e = f"string({v})" if req else f"str({v})"
        inp.append(f"\t\t{dom(nm)}: {e},")

    flt, imports_uuid = [], False
    cases = []
    for x in r["filters"]:
        if not isinstance(x, dict):
            continue
        g, k = up(x["n"]), x["k"]
        fld = dom(x["n"])
        if k == "enum":
            cases.append(f"\tf.{fld} = str(p.{g})\n\toneOf(&errs, \"{x['n']}\", f.{fld}, {', '.join(chr(34) + e + chr(34) for e in x['enum'])})")
        elif k == "bool":
            cases.append(f"\tf.{fld} = p.{g}")
        elif k == "uuid":
            cases.append(f"\tif p.{g} != nil {{\n\t\tf.{fld} = p.{g}.String()\n\t}}")
        else:
            cases.append(f"\tif p.{g} != nil {{\n\t\tf.{fld} = *p.{g}\n\t}}")
    body = "\n".join(cases)
    return f'''
// ---- {r['noun']}

func toGen{n}(d {pkg}.{d}) gen.{n} {{
	return gen.{n}{{
{chr(10).join(ent)}
	}}
}}

func {ln}Input(b gen.{n}Input) {pkg}.Input {{
	return {pkg}.Input{{
{chr(10).join(inp)}
	}}
}}

func {ln}Filter(p gen.List{r['plural']}Params) ({pkg}.Filter, crud.FieldErrors) {{
	var errs crud.FieldErrors
	f := {pkg}.Filter{{}}
{body}
	return f, errs
}}
'''
