-- 000004_academic.up.sql
-- Academic module: students, teachers, classes, enrollments, grades

-- Academic years
CREATE TABLE academic_years (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,           -- '2024-2025'
    code VARCHAR(20) NOT NULL,            -- '2024-25'
    start_date DATE NOT NULL,
    end_date DATE NOT NULL,
    is_current BOOLEAN DEFAULT false,
    is_active BOOLEAN DEFAULT true,
    settings JSONB DEFAULT '{}'::jsonb,   -- grading scale, terms, etc.
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE (institute_id, code)
);

COMMENT ON TABLE academic_years IS 'Academic years/sessions';
CREATE INDEX idx_academic_years_institute ON academic_years(institute_id, is_active);

-- Terms (semesters, quarters, trimesters)
CREATE TABLE terms (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    academic_year_id UUID NOT NULL REFERENCES academic_years(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,           -- 'Fall 2024', 'Semester 1'
    code VARCHAR(20) NOT NULL,            -- 'fall', 's1', 'q1'
    start_date DATE NOT NULL,
    end_date DATE NOT NULL,
    is_current BOOLEAN DEFAULT false,
    is_active BOOLEAN DEFAULT true,
    settings JSONB DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE (academic_year_id, code)
);

COMMENT ON TABLE terms IS 'Academic terms within a year';
CREATE INDEX idx_terms_year ON terms(academic_year_id, is_active);

-- Departments
CREATE TABLE departments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    parent_id UUID REFERENCES departments(id) ON DELETE SET NULL,
    name VARCHAR(200) NOT NULL,
    code VARCHAR(20) NOT NULL,
    description TEXT,
    head_id UUID REFERENCES users(id) ON DELETE SET NULL,
    is_active BOOLEAN DEFAULT true,
    settings JSONB DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE (institute_id, code)
);

COMMENT ON TABLE departments IS 'Academic departments';
CREATE INDEX idx_departments_institute ON departments(institute_id, is_active);
CREATE INDEX idx_departments_parent ON departments(parent_id);

-- Subjects/Courses
CREATE TABLE subjects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    department_id UUID REFERENCES departments(id) ON DELETE SET NULL,
    code VARCHAR(30) NOT NULL,            -- 'MATH101', 'ENG201'
    name VARCHAR(200) NOT NULL,
    description TEXT,
    credits DECIMAL(4,2) DEFAULT 0,
    grade_levels INTEGER[],               -- [9,10,11,12] or [1,2,3,4,5,6]
    prerequisites UUID[],                 -- subject IDs
    is_core BOOLEAN DEFAULT false,
    is_active BOOLEAN DEFAULT true,
    settings JSONB DEFAULT '{}'::jsonb,   -- syllabus, resources, assessment weights
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE (institute_id, code)
);

COMMENT ON TABLE subjects IS 'Subjects/courses offered';
CREATE INDEX idx_subjects_institute ON subjects(institute_id, is_active);
CREATE INDEX idx_subjects_department ON subjects(department_id);

-- Classes/Sections
CREATE TABLE classes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    academic_year_id UUID NOT NULL REFERENCES academic_years(id) ON DELETE CASCADE,
    term_id UUID REFERENCES terms(id) ON DELETE SET NULL,
    subject_id UUID NOT NULL REFERENCES subjects(id) ON DELETE RESTRICT,
    teacher_id UUID REFERENCES users(id) ON DELETE SET NULL,
    room_id UUID,                         -- references rooms table (later)
    
    name VARCHAR(100) NOT NULL,           -- 'Grade 10 A', 'AP Calculus BC'
    code VARCHAR(30) NOT NULL,            -- '10A', 'CALC-BC-1'
    section VARCHAR(10),                  -- 'A', 'B', '1', '2'
    
    schedule JSONB DEFAULT '[]'::jsonb,   -- [{"day":1,"start":"08:00","end":"09:00","room":"101"}]
    max_students INTEGER DEFAULT 30,
    min_students INTEGER DEFAULT 5,
    
    status VARCHAR(20) DEFAULT 'scheduled', -- scheduled, active, completed, cancelled
    is_active BOOLEAN DEFAULT true,
    settings JSONB DEFAULT '{}'::jsonb,   -- grading policy, attendance policy, resources
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE (institute_id, academic_year_id, code)
);

COMMENT ON TABLE classes IS 'Class sections for a term';
COMMENT ON COLUMN classes.schedule IS 'Weekly schedule: day (1-7), start/end time, room';
CREATE INDEX idx_classes_institute_year ON classes(institute_id, academic_year_id, status);
CREATE INDEX idx_classes_teacher ON classes(teacher_id, status) WHERE teacher_id IS NOT NULL;
CREATE INDEX idx_classes_subject ON classes(subject_id, status);

-- Class enrollments
CREATE TABLE class_enrollments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    class_id UUID NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
    student_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    enrolled_at TIMESTAMPTZ DEFAULT now(),
    dropped_at TIMESTAMPTZ,
    drop_reason VARCHAR(200),
    status VARCHAR(20) DEFAULT 'enrolled', -- enrolled, dropped, completed, waitlisted
    grade VARCHAR(10),                    -- Final letter grade
    numeric_grade DECIMAL(5,2),           -- Final numeric grade
    metadata JSONB DEFAULT '{}'::jsonb,   -- accommodations, notes
    UNIQUE (class_id, student_id)
);

COMMENT ON TABLE class_enrollments IS 'Student enrollment in class sections';
CREATE INDEX idx_enrollments_class ON class_enrollments(class_id, status);
CREATE INDEX idx_enrollments_student ON class_enrollments(student_id, status);

-- Assignments
CREATE TABLE assignments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    class_id UUID NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
    created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    
    title VARCHAR(200) NOT NULL,
    description TEXT,
    type VARCHAR(30) DEFAULT 'homework',  -- homework, quiz, test, project, exam, participation
    category VARCHAR(50),                 -- 'Homework', 'Quizzes', 'Tests', 'Projects'
    
    max_points DECIMAL(6,2) DEFAULT 100,
    weight DECIMAL(5,2) DEFAULT 1.0,      -- Weight in grade calculation
    
    assigned_at TIMESTAMPTZ NOT NULL,
    due_at TIMESTAMPTZ NOT NULL,
    available_from TIMESTAMPTZ,
    available_until TIMESTAMPTZ,
    
    submission_type VARCHAR(20) DEFAULT 'online', -- online, in_class, paper, external
    allowed_file_types VARCHAR(100)[],    -- ['pdf', 'docx', 'jpg']
    max_file_size_mb INTEGER DEFAULT 10,
    max_submissions INTEGER DEFAULT 1,
    
    is_published BOOLEAN DEFAULT false,
    is_graded BOOLEAN DEFAULT false,
    settings JSONB DEFAULT '{}'::jsonb,   -- rubric, peer review, group work
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE assignments IS 'Assignments, quizzes, tests, projects';
CREATE INDEX idx_assignments_class ON assignments(class_id, due_at);
CREATE INDEX idx_assignments_due ON assignments(due_at) WHERE is_published = true;

-- Submissions
CREATE TABLE submissions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    assignment_id UUID NOT NULL REFERENCES assignments(id) ON DELETE CASCADE,
    student_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    content TEXT,
    file_urls TEXT[],
    metadata JSONB DEFAULT '{}'::jsonb,
    
    submitted_at TIMESTAMPTZ DEFAULT now(),
    graded_at TIMESTAMPTZ,
    graded_by UUID REFERENCES users(id) ON DELETE SET NULL,
    
    points_earned DECIMAL(6,2),
    feedback TEXT,
    rubric_scores JSONB,                  -- {"criteria1": 4, "criteria2": 3}
    status VARCHAR(20) DEFAULT 'submitted', -- submitted, late, graded, returned, missing
    attempt_number INTEGER DEFAULT 1,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE (assignment_id, student_id, attempt_number)
);

COMMENT ON TABLE submissions IS 'Student assignment submissions';
CREATE INDEX idx_submissions_assignment ON submissions(assignment_id, status);
CREATE INDEX idx_submissions_student ON submissions(student_id, submitted_at DESC);

-- Grades
CREATE TABLE grades (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    enrollment_id UUID NOT NULL REFERENCES class_enrollments(id) ON DELETE CASCADE,
    assignment_id UUID REFERENCES assignments(id) ON DELETE SET NULL,
    
    points_earned DECIMAL(6,2) NOT NULL,
    points_possible DECIMAL(6,2) NOT NULL,
    percentage DECIMAL(5,2) GENERATED ALWAYS AS (
        CASE WHEN points_possible > 0 
        THEN ROUND((points_earned / points_possible) * 100, 2)
        ELSE 0 END
    ) STORED,
    
    letter_grade VARCHAR(10),
    graded_at TIMESTAMPTZ DEFAULT now(),
    graded_by UUID REFERENCES users(id) ON DELETE SET NULL,
    feedback TEXT,
    is_published BOOLEAN DEFAULT false,
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE grades IS 'Individual grade entries';
CREATE INDEX idx_grades_enrollment ON grades(enrollment_id, graded_at DESC);
CREATE INDEX idx_grades_assignment ON grades(assignment_id);

-- Attendance
CREATE TABLE attendance (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    class_id UUID NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
    student_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    date DATE NOT NULL,
    period INTEGER,                       -- Period number (1-8)
    
    status VARCHAR(20) NOT NULL DEFAULT 'present', -- present, absent, late, excused, early_dismissal
    minutes_late INTEGER DEFAULT 0,
    reason TEXT,
    marked_by UUID REFERENCES users(id) ON DELETE SET NULL,
    marked_at TIMESTAMPTZ DEFAULT now(),
    
    UNIQUE (class_id, student_id, date, period)
);

COMMENT ON TABLE attendance IS 'Daily attendance records';
COMMENT ON COLUMN attendance.period IS 'Class period (null for full day)';
CREATE INDEX idx_attendance_class_date ON attendance(class_id, date);
CREATE INDEX idx_attendance_student_date ON attendance(student_id, date DESC);
CREATE INDEX idx_attendance_status ON attendance(status) WHERE status != 'present';

-- Attendance summary (materialized view refreshed daily)
CREATE MATERIALIZED VIEW attendance_summary AS
SELECT 
    a.student_id,
    a.class_id,
    COUNT(*) FILTER (WHERE a.status = 'present') as present_count,
    COUNT(*) FILTER (WHERE a.status = 'absent') as absent_count,
    COUNT(*) FILTER (WHERE a.status = 'late') as late_count,
    COUNT(*) FILTER (WHERE a.status = 'excused') as excused_count,
    COUNT(*) as total_sessions,
    ROUND(
        COUNT(*) FILTER (WHERE a.status IN ('present', 'late', 'excused')) * 100.0 / 
        NULLIF(COUNT(*), 0), 2
    ) as attendance_rate
FROM attendance a
GROUP BY a.student_id, a.class_id;

CREATE UNIQUE INDEX idx_attendance_summary ON attendance_summary(student_id, class_id);

-- Enable RLS
ALTER TABLE academic_years ENABLE ROW LEVEL SECURITY;
ALTER TABLE terms ENABLE ROW LEVEL SECURITY;
ALTER TABLE departments ENABLE ROW LEVEL SECURITY;
ALTER TABLE subjects ENABLE ROW LEVEL SECURITY;
ALTER TABLE classes ENABLE ROW LEVEL SECURITY;
ALTER TABLE class_enrollments ENABLE ROW LEVEL SECURITY;
ALTER TABLE assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE submissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE grades ENABLE ROW LEVEL SECURITY;
ALTER TABLE attendance ENABLE ROW LEVEL SECURITY;

-- RLS Policies (tenant isolation)
CREATE POLICY academic_tenant ON academic_years
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY terms_tenant ON terms
    USING (academic_year_id IN (SELECT id FROM academic_years WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

CREATE POLICY departments_tenant ON departments
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY subjects_tenant ON subjects
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY classes_tenant ON classes
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY enrollments_tenant ON class_enrollments
    USING (class_id IN (SELECT id FROM classes WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

CREATE POLICY assignments_tenant ON assignments
    USING (class_id IN (SELECT id FROM classes WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

CREATE POLICY submissions_tenant ON submissions
    USING (student_id IN (SELECT id FROM users WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

CREATE POLICY grades_tenant ON grades
    USING (enrollment_id IN (SELECT id FROM class_enrollments WHERE class_id IN (SELECT id FROM classes WHERE institute_id = current_setting('app.current_institute_id', true)::UUID)));

CREATE POLICY attendance_tenant ON attendance
    USING (class_id IN (SELECT id FROM classes WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

-- Self-access policies for students/parents
CREATE POLICY enrollments_self ON class_enrollments
    USING (student_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY submissions_self ON submissions
    USING (student_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY grades_self ON grades
    USING (enrollment_id IN (SELECT id FROM class_enrollments WHERE student_id = current_setting('app.current_user_id', true)::UUID));

CREATE POLICY attendance_self ON attendance
    USING (student_id = current_setting('app.current_user_id', true)::UUID);