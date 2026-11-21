-- Academic masters, batch 2: class-section and subject-class mappings (per academic year), textbooks and chapters
-- (ACA-30, ACA-31, ACA-26, ACA-27). Every table is institute-scoped with row-level security and soft deletes, like batch 1.

CREATE TABLE class_section_mappings (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    year_id    uuid NOT NULL,
    class_id   uuid NOT NULL,
    section_id uuid NOT NULL,
    capacity   integer CHECK (capacity IS NULL OR capacity BETWEEN 1 AND 1000),
    room_id    uuid,
    remarks    text CHECK (remarks IS NULL OR length(remarks) <= 500),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid,
    updated_by uuid,
    deleted_at timestamptz,
    FOREIGN KEY (tenant_id, year_id)    REFERENCES academic_years (tenant_id, id),
    FOREIGN KEY (tenant_id, class_id)   REFERENCES classes (tenant_id, id),
    FOREIGN KEY (tenant_id, section_id) REFERENCES sections (tenant_id, id),
    FOREIGN KEY (tenant_id, room_id)    REFERENCES rooms (tenant_id, id),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE class_section_mappings IS 'Which sections a class has in an academic year, with capacity and room (ACA-30). The class teacher is added when the teacher master exists.';
CREATE UNIQUE INDEX class_section_mappings_uq ON class_section_mappings (tenant_id, year_id, class_id, section_id) WHERE deleted_at IS NULL;
CREATE INDEX class_section_mappings_class_idx ON class_section_mappings (tenant_id, class_id) WHERE deleted_at IS NULL;
CREATE TRIGGER class_section_mappings_updated_at BEFORE UPDATE ON class_section_mappings FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE class_section_mappings ENABLE ROW LEVEL SECURITY;
CREATE POLICY class_section_mappings_tenant ON class_section_mappings USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE subject_class_mappings (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    year_id          uuid NOT NULL,
    class_id         uuid NOT NULL,
    subject_id       uuid NOT NULL,
    section_id       uuid,
    periods_per_week integer CHECK (periods_per_week IS NULL OR periods_per_week BETWEEN 0 AND 60),
    is_elective      boolean NOT NULL DEFAULT false,
    is_examined      boolean NOT NULL DEFAULT true,
    is_assessed      boolean NOT NULL DEFAULT true,
    is_grade_only    boolean NOT NULL DEFAULT false,
    has_practical    boolean NOT NULL DEFAULT false,
    on_report_card   boolean NOT NULL DEFAULT true,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    created_by       uuid,
    updated_by       uuid,
    deleted_at       timestamptz,
    FOREIGN KEY (tenant_id, year_id)    REFERENCES academic_years (tenant_id, id),
    FOREIGN KEY (tenant_id, class_id)   REFERENCES classes (tenant_id, id),
    FOREIGN KEY (tenant_id, subject_id) REFERENCES subjects (tenant_id, id),
    FOREIGN KEY (tenant_id, section_id) REFERENCES sections (tenant_id, id),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE subject_class_mappings IS 'Which subjects a class studies in an academic year (ACA-31). A null section means the whole class; a section row overrides it for that section.';
CREATE UNIQUE INDEX subject_class_mappings_uq ON subject_class_mappings (tenant_id, year_id, class_id, subject_id, COALESCE(section_id, '00000000-0000-0000-0000-000000000000'::uuid)) WHERE deleted_at IS NULL;
CREATE INDEX subject_class_mappings_class_idx ON subject_class_mappings (tenant_id, class_id) WHERE deleted_at IS NULL;
CREATE TRIGGER subject_class_mappings_updated_at BEFORE UPDATE ON subject_class_mappings FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE subject_class_mappings ENABLE ROW LEVEL SECURITY;
CREATE POLICY subject_class_mappings_tenant ON subject_class_mappings USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE textbooks (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code       text NOT NULL CHECK (length(btrim(code)) BETWEEN 1 AND 20),
    title      text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 200),
    class_id   uuid NOT NULL,
    subject_id uuid NOT NULL,
    publisher  text CHECK (publisher IS NULL OR length(publisher) <= 100),
    author     text CHECK (author IS NULL OR length(author) <= 100),
    isbn       text CHECK (isbn IS NULL OR isbn ~ '^[0-9Xx-]{10,17}$'),
    book_url   text CHECK (book_url IS NULL OR (length(book_url) <= 500 AND book_url ~* '^https?://')),
    notes      text CHECK (notes IS NULL OR length(notes) <= 500),
    status     text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid,
    updated_by uuid,
    deleted_at timestamptz,
    FOREIGN KEY (tenant_id, class_id)   REFERENCES classes (tenant_id, id),
    FOREIGN KEY (tenant_id, subject_id) REFERENCES subjects (tenant_id, id),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE textbooks IS 'Textbooks of a class and subject (ACA-26). The PDF upload waits for file storage; book_url is an online copy and must be http(s).';
CREATE UNIQUE INDEX textbooks_tenant_code_uq ON textbooks (tenant_id, lower(code)) WHERE deleted_at IS NULL;
CREATE INDEX textbooks_class_subject_idx ON textbooks (tenant_id, class_id, subject_id) WHERE deleted_at IS NULL;
CREATE TRIGGER textbooks_updated_at BEFORE UPDATE ON textbooks FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE textbooks ENABLE ROW LEVEL SECURITY;
CREATE POLICY textbooks_tenant ON textbooks USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());

CREATE TABLE textbook_chapters (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    textbook_id uuid NOT NULL,
    sort_order  integer NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN 0 AND 100000),
    chapter_no  text CHECK (chapter_no IS NULL OR length(btrim(chapter_no)) BETWEEN 1 AND 20),
    title       text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 200),
    periods     integer CHECK (periods IS NULL OR periods BETWEEN 0 AND 200),
    from_page   integer CHECK (from_page IS NULL OR from_page BETWEEN 1 AND 10000),
    to_page     integer CHECK (to_page IS NULL OR to_page BETWEEN 1 AND 10000),
    is_optional boolean NOT NULL DEFAULT false,
    summary     text CHECK (summary IS NULL OR length(summary) <= 1000),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid,
    updated_by  uuid,
    deleted_at  timestamptz,
    CHECK (from_page IS NULL OR to_page IS NULL OR to_page >= from_page),
    FOREIGN KEY (tenant_id, textbook_id) REFERENCES textbooks (tenant_id, id),
    UNIQUE (tenant_id, id)
);
COMMENT ON TABLE textbook_chapters IS 'Chapters of a textbook (ACA-27). is_optional chapters are dropped first when time is short and do not count towards the periods a book needs.';
CREATE UNIQUE INDEX textbook_chapters_no_uq ON textbook_chapters (tenant_id, textbook_id, lower(chapter_no)) WHERE chapter_no IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX textbook_chapters_book_idx ON textbook_chapters (tenant_id, textbook_id, sort_order) WHERE deleted_at IS NULL;
CREATE TRIGGER textbook_chapters_updated_at BEFORE UPDATE ON textbook_chapters FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE textbook_chapters ENABLE ROW LEVEL SECURITY;
CREATE POLICY textbook_chapters_tenant ON textbook_chapters USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant());
