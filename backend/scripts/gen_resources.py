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
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from resource_dsl import maps, schemas  # noqa: E402

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
        perm="aca.classes", pkg="classes", domain="Class", field="classes", noun="class", in_use=True,
        sort="code, name, displayOrder, youngestAgeMonths, createdAt",
        filters=[("status", "string", "enum: [active, inactive]")],
        entity_required="id, code, name, divisionId, divisionName, displayOrder, youngestAgeMonths, oldestAgeMonths, description, status, createdAt, updatedAt",
        entity="""
        id: { type: string, format: uuid }
        code: { type: string, maxLength: 20 }
        name: { type: string, maxLength: 100 }
        divisionId: { type: string, format: uuid, nullable: true }
        divisionName: { type: string, nullable: true }
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
        divisionId: { type: string, format: uuid, nullable: true, description: The school division this class belongs to. }
        displayOrder: { type: integer, minimum: 0, maximum: 100000, default: 0 }
        youngestAgeMonths: { type: integer, nullable: true, minimum: 0, maximum: 600 }
        oldestAgeMonths: { type: integer, nullable: true, minimum: 0, maximum: 600, description: Not below the youngest age. }
        description: { type: string, nullable: true, maxLength: 500 }
        status: { type: string, enum: [active, inactive], default: active }""",
    ),
    dict(
        stem="sections", nouns="sections", module="aca", name="Section", plural="Sections", path="/sections", idParam="sectionId",
        perm="aca.sections", pkg="sections", domain="Section", field="sections", noun="section", in_use=True,
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
        perm="aca.subjects", pkg="subjects", domain="Subject", field="subjects", noun="subject", in_use=True,
        sort="code, name, category, area, credits, createdAt",
        filters=[("status", "string", "enum: [active, inactive]"), ("category", "string", "")],
        entity_required="id, code, name, shortName, color, category, area, scholastic, coScholastic, nonScholastic, credits, description, status, createdAt, updatedAt",
        entity="""
        id: { type: string, format: uuid }
        code: { type: string, maxLength: 20 }
        name: { type: string, maxLength: 100 }
        shortName: { type: string, nullable: true, maxLength: 20 }
        color: { type: string, nullable: true, pattern: '^#[0-9A-Fa-f]{6}$' }
        category: { type: string, enum: [core, elective, language, optional, vocational] }
        area: { type: string, enum: [languages, mathematics, science, social_science, arts, physical_education, technology, vocational, life_skills, other] }
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
        category: { type: string, enum: [core, elective, language, optional, vocational] }
        area: { type: string, enum: [languages, mathematics, science, social_science, arts, physical_education, technology, vocational, life_skills, other] }
        scholastic: { type: boolean, default: false }
        coScholastic: { type: boolean, default: false }
        nonScholastic: { type: boolean, default: false }
        credits: { type: number, format: double, minimum: 0, maximum: 99.9, description: Kept to one decimal. }
        description: { type: string, nullable: true, maxLength: 500 }
        status: { type: string, enum: [active, inactive], default: active }""",
    ),
]

STATUS = dict(n="status", k="enum", enum=["active", "inactive"], default="active")
YEAR = [dict(n="yearId", k="uuid", req=True), dict(n="yearName", k="string", ro=True)]

RESOURCES += [
    dict(
        stem="divisions", nouns="school divisions", module="aca", name="SchoolDivision", plural="SchoolDivisions", path="/school-divisions", idParam="divisionId",
        perm="aca.school_divisions", pkg="divisions", domain="Division", field="schoolDivisions", noun="school division", in_use=True,
        sort="code, name, displayOrder, createdAt",
        filters=[dict(n="status", k="enum", enum=["active", "inactive"])],
        fields=[
            dict(n="code", k="string", req=True, max=20),
            dict(n="name", k="string", req=True, max=100),
            dict(n="displayOrder", k="int", default=0, lo=0, hi=100000),
            dict(n="description", k="string?", max=500),
            STATUS,
        ],
    ),
    dict(
        stem="campuses", nouns="campuses", module="aca", name="Campus", plural="Campuses", path="/campuses", idParam="campusId",
        perm="aca.campuses", pkg="campuses", domain="Campus", field="campuses", noun="campus", in_use=False,
        sort="code, name, createdAt",
        filters=[dict(n="status", k="enum", enum=["active", "inactive"])],
        fields=[
            dict(n="code", k="string?", max=20),
            dict(n="name", k="string", req=True, max=100),
            dict(n="checkInRadiusM", k="int", req=True, lo=10, hi=5000, desc="Radius of the staff check-in circle in metres."),
            dict(n="latitude", k="number?", lo=-90, hi=90),
            dict(n="longitude", k="number?", lo=-180, hi=180, desc="Set together with latitude or leave both empty."),
            dict(n="staffDueIn", k="string?", pattern="^([01][0-9]|2[0-3]):[0-5][0-9]$", desc="HH:MM; only when staff of this campus are due in at a different time from the school."),
            dict(n="address", k="string?", max=300),
            dict(n="remarks", k="string?", max=500),
            STATUS,
        ],
    ),
    dict(
        stem="holidays", nouns="holidays", module="aca", name="Holiday", plural="Holidays", path="/holidays", idParam="holidayId",
        perm="aca.holiday_calendars", pkg="holidays", domain="Holiday", field="holidays", noun="holiday", in_use=False,
        sort="title, startDate, endDate, createdAt",
        filters=[dict(n="yearId", k="uuid"), dict(n="holidayType", k="enum", enum=["public", "school", "regional", "restricted", "other"]), dict(n="status", k="enum", enum=["active", "inactive"])],
        fields=YEAR + [
            dict(n="title", k="string", req=True, max=100),
            dict(n="holidayType", k="enum", req=True, enum=["public", "school", "regional", "restricted", "other"]),
            dict(n="startDate", k="date", req=True, desc="Inside the academic year unless the holiday recurs every year."),
            dict(n="endDate", k="date", req=True),
            dict(n="isOptional", k="bool", default=False, desc="Staff and students may choose to take it."),
            dict(n="isRecurringAnnual", k="bool", default=False),
            dict(n="applicableFor", k="string?", max=100, desc="Free text, for example a class group or campus."),
            dict(n="description", k="string?", max=500),
            STATUS,
        ],
    ),
    dict(
        stem="events", nouns="calendar events", module="aca", name="CalendarEvent", plural="CalendarEvents", path="/calendar-events", idParam="eventId",
        perm="aca.academic_calendars", pkg="events", domain="Event", field="calendarEvents", noun="calendar event", in_use=False,
        sort="title, startDate, endDate, createdAt",
        filters=[dict(n="yearId", k="uuid"), dict(n="eventType", k="enum", enum=["academic", "exam", "event", "meeting", "activity", "other"]), dict(n="status", k="enum", enum=["active", "inactive"])],
        fields=YEAR + [
            dict(n="title", k="string", req=True, max=100),
            dict(n="eventType", k="enum", req=True, enum=["academic", "exam", "event", "meeting", "activity", "other"]),
            dict(n="startDate", k="date", req=True, desc="Inside the academic year."),
            dict(n="endDate", k="date", req=True),
            dict(n="isAllDay", k="bool", default=True),
            dict(n="isInstructionalDay", k="bool", default=False, desc="Classes are held."),
            dict(n="isExaminationDay", k="bool", default=False),
            dict(n="venue", k="string?", max=100),
            dict(n="description", k="string?", max=500),
            STATUS,
        ],
    ),
    dict(
        stem="electives", nouns="elective groups", module="aca", name="ElectiveGroup", plural="ElectiveGroups", path="/elective-groups", idParam="groupId",
        perm="aca.elective_groups", pkg="electives", domain="Group", field="electiveGroups", noun="elective group", in_use=True,
        sort="code, name, displayOrder, createdAt",
        filters=[dict(n="status", k="enum", enum=["active", "inactive"])],
        fields=[
            dict(n="code", k="string?", max=20),
            dict(n="name", k="string", req=True, max=100),
            dict(n="minSubjects", k="int", req=True, lo=0, hi=50, desc="A student must take at least this many."),
            dict(n="maxSubjects", k="int", req=True, lo=1, hi=50, desc="A student may take at most this many; not below the minimum."),
            dict(n="enforceLimits", k="bool", default=True, desc="Refuse choices outside the limits."),
            dict(n="displayOrder", k="int", default=0, lo=0, hi=100000),
            dict(n="description", k="string?", max=500),
            STATUS,
        ],
    ),
]

RESOURCES += [
    dict(
        stem="classsections", nouns="class-section mappings", module="aca", name="ClassSectionMapping", plural="ClassSectionMappings", path="/class-section-mappings", idParam="mappingId",
        perm="aca.class_section_mappings", pkg="classsections", domain="ClassSection", field="classSectionMappings", noun="class-section mapping", in_use=True,
        sort="className, sectionName, capacity, createdAt",
        filters=[dict(n="yearId", k="uuid"), dict(n="classId", k="uuid"), dict(n="sectionId", k="uuid")],
        fields=YEAR + [
            dict(n="classId", k="uuid", req=True),
            dict(n="classCode", k="string", ro=True),
            dict(n="className", k="string", ro=True),
            dict(n="sectionId", k="uuid", req=True),
            dict(n="sectionCode", k="string", ro=True),
            dict(n="sectionName", k="string", ro=True),
            dict(n="capacity", k="int?", lo=1, hi=1000, desc="How many students the section can hold."),
            dict(n="roomId", k="uuid?", desc="The room the section normally uses."),
            dict(n="roomName", k="string?", ro=True),
            dict(n="remarks", k="string?", max=500),
        ],
    ),
    dict(
        stem="subjectclasses", nouns="subject-class mappings", module="aca", name="SubjectClassMapping", plural="SubjectClassMappings", path="/subject-class-mappings", idParam="mappingId",
        perm="aca.subject_class_mappings", pkg="subjectclasses", domain="SubjectClass", field="subjectClassMappings", noun="subject-class mapping", in_use=False,
        sort="className, subjectName, periodsPerWeek, createdAt",
        filters=[dict(n="yearId", k="uuid"), dict(n="classId", k="uuid"), dict(n="subjectId", k="uuid"), dict(n="sectionId", k="uuid")],
        fields=YEAR + [
            dict(n="classId", k="uuid", req=True),
            dict(n="classCode", k="string", ro=True),
            dict(n="className", k="string", ro=True),
            dict(n="subjectId", k="uuid", req=True),
            dict(n="subjectCode", k="string", ro=True),
            dict(n="subjectName", k="string", ro=True),
            dict(n="subjectArea", k="string", ro=True),
            dict(n="sectionId", k="uuid?", desc="Leave empty for the whole class. A section must already be mapped to the class for that year."),
            dict(n="sectionName", k="string?", ro=True),
            dict(n="periodsPerWeek", k="int?", lo=0, hi=60),
            dict(n="isElective", k="bool", default=False),
            dict(n="electiveGroupId", k="uuid?", desc="The elective group this subject is offered in (only for an elective subject). Students choose their subjects of a group from these (STU-04)."),
            dict(n="electiveGroupName", k="string?", ro=True),
            dict(n="isExamined", k="bool", default=True),
            dict(n="isAssessed", k="bool", default=True),
            dict(n="isGradeOnly", k="bool", default=False, desc="Reported as a grade, not marks."),
            dict(n="hasPractical", k="bool", default=False),
            dict(n="onReportCard", k="bool", default=True),
        ],
    ),
    dict(
        stem="textbooks", nouns="textbooks", module="aca", name="Textbook", plural="Textbooks", path="/textbooks", idParam="textbookId",
        perm="aca.textbooks", pkg="textbooks", domain="Textbook", field="textbooks", noun="textbook", in_use=True,
        sort="code, title, className, subjectName, createdAt",
        filters=[dict(n="classId", k="uuid"), dict(n="subjectId", k="uuid"), dict(n="status", k="enum", enum=["active", "inactive"])],
        fields=[
            dict(n="code", k="string", req=True, max=20),
            dict(n="title", k="string", req=True, max=200),
            dict(n="classId", k="uuid", req=True),
            dict(n="classCode", k="string", ro=True),
            dict(n="className", k="string", ro=True),
            dict(n="subjectId", k="uuid", req=True),
            dict(n="subjectCode", k="string", ro=True),
            dict(n="subjectName", k="string", ro=True),
            dict(n="publisher", k="string?", max=100),
            dict(n="author", k="string?", max=100),
            dict(n="isbn", k="string?", max=17, desc="10 to 17 digits, hyphens or an X."),
            dict(n="bookUrl", k="string?", max=500, desc="An online copy; must start with http:// or https://."),
            dict(n="notes", k="string?", max=500),
            STATUS,
            dict(n="chapterCount", k="int", ro=True),
            dict(n="periodsNeeded", k="int", ro=True, desc="Periods of the chapters that are not optional."),
        ],
    ),
    dict(
        stem="chapters", nouns="textbook chapters", module="aca", name="TextbookChapter", plural="TextbookChapters", path="/textbook-chapters", idParam="chapterId",
        perm="aca.textbook_chapters", pkg="chapters", domain="Chapter", field="textbookChapters", noun="textbook chapter", in_use=False,
        sort="sortOrder, title, periods, createdAt",
        filters=[dict(n="textbookId", k="uuid")],
        fields=[
            dict(n="textbookId", k="uuid", req=True),
            dict(n="textbookCode", k="string", ro=True),
            dict(n="textbookTitle", k="string", ro=True),
            dict(n="sortOrder", k="int", default=0, lo=0, hi=100000, desc="Position in the book."),
            dict(n="chapterNo", k="string?", max=20, desc="Unique within the book when given."),
            dict(n="title", k="string", req=True, max=200),
            dict(n="periods", k="int?", lo=0, hi=200),
            dict(n="fromPage", k="int?", lo=1, hi=10000),
            dict(n="toPage", k="int?", lo=1, hi=10000, desc="Not before the first page."),
            dict(n="isOptional", k="bool", default=False, desc="Dropped first when time is short; not counted in the periods a book needs."),
            dict(n="summary", k="string?", max=1000),
        ],
    ),
]

RESOURCES += [
    dict(
        stem="students", nouns="students", module="stu", name="Student", plural="Students", path="/students", idParam="studentId",
        perm="stu.students", pkg="students", domain="Student", field="students", noun="student", in_use=True, audit_read=True,
        sort="registrationCode, fullName, dateOfBirth, createdAt",
        filters=[dict(n="status", k="enum", enum=["active", "paused", "transferred", "alumni"]), dict(n="gender", k="enum", enum=["male", "female", "other"])],
        fields=[
            dict(n="registrationCode", k="string", req=True, max=30),
            dict(n="firstName", k="string", req=True, max=100),
            dict(n="middleName", k="string?", max=100),
            dict(n="lastName", k="string?", max=100),
            dict(n="fullName", k="string", ro=True),
            dict(n="gender", k="enum", req=True, enum=["male", "female", "other"]),
            dict(n="dateOfBirth", k="date", req=True, desc="Not in the future and not more than 30 years ago."),
            dict(n="admissionDate", k="date?"),
            dict(n="mobile", k="string?", max=20, desc="The number the school messages first."),
            dict(n="parentEmail", k="string?", max=254),
            dict(n="studentEmail", k="string?", max=254),
            dict(n="apaarId", k="string?", max=12, desc="12 digits."),
            dict(n="status", k="enum", enum=["active", "paused", "transferred", "alumni"], default="active"),
            dict(n="remarks", k="string?", max=500),
            dict(n="currentClassName", k="string?", ro=True, desc="From the enrollment in the current academic year."),
            dict(n="currentSectionName", k="string?", ro=True),
            dict(n="currentRollNumber", k="string?", ro=True),
        ],
    ),
    dict(
        stem="enrollments", nouns="student enrollments", module="stu", name="StudentEnrollment", plural="StudentEnrollments", path="/student-enrollments", idParam="enrollmentId",
        perm="stu.students", pkg="enrollments", domain="Enrollment", field="studentEnrollments", noun="student enrollment", in_use=False, audit_read=True,
        sort="studentName, className, sectionName, rollNumber, createdAt",
        filters=[dict(n="studentId", k="uuid"), dict(n="yearId", k="uuid"), dict(n="classId", k="uuid"), dict(n="sectionId", k="uuid"),
                 dict(n="status", k="enum", enum=["enrolled", "withdrawn", "completed"])],
        fields=[
            dict(n="studentId", k="uuid", req=True),
            dict(n="registrationCode", k="string", ro=True),
            dict(n="studentName", k="string", ro=True),
            dict(n="yearId", k="uuid", req=True),
            dict(n="yearName", k="string", ro=True),
            dict(n="classId", k="uuid", req=True),
            dict(n="className", k="string", ro=True),
            dict(n="sectionId", k="uuid", req=True, desc="Must already be mapped to the class for that year."),
            dict(n="sectionName", k="string", ro=True),
            dict(n="rollNumber", k="string?", max=20, desc="Unique within the class and section."),
            dict(n="enrollmentNumber", k="string?", max=30, desc="Unique across the institute."),
            dict(n="enrollmentDate", k="date?"),
            dict(n="status", k="enum", enum=["enrolled", "withdrawn", "completed"], default="enrolled"),
        ],
    ),
]

RESOURCES += [
    dict(
        stem="guardians", nouns="guardians", module="stu", name="Guardian", plural="Guardians", path="/guardians", idParam="guardianId",
        perm="stu.students", pkg="guardians", domain="Guardian", field="guardians", noun="guardian", in_use=True, audit_read=True,
        sort="name, phone, createdAt",
        filters=[dict(n="phone", k="string")],
        fields=[
            dict(n="name", k="string", req=True, max=150),
            dict(n="phone", k="string", req=True, max=20, desc="7 to 20 digits; may include +, spaces, brackets or hyphens."),
            dict(n="email", k="string?", max=254),
            dict(n="studentCount", k="int", ro=True, desc="How many students this guardian is linked to."),
        ],
    ),
    dict(
        stem="links", nouns="student guardians", module="stu", name="StudentGuardian", plural="StudentGuardians", path="/student-guardians", idParam="linkId",
        perm="stu.students", pkg="links", domain="Link", field="studentGuardians", noun="student guardian", in_use=False, audit_read=True,
        sort="studentName, guardianName, relation, createdAt",
        filters=[dict(n="studentId", k="uuid"), dict(n="guardianId", k="uuid")],
        fields=[
            dict(n="studentId", k="uuid", req=True),
            dict(n="registrationCode", k="string", ro=True),
            dict(n="studentName", k="string", ro=True),
            dict(n="guardianId", k="uuid", req=True),
            dict(n="guardianName", k="string", ro=True),
            dict(n="guardianPhone", k="string", ro=True),
            dict(n="relation", k="enum", req=True, enum=["father", "mother", "guardian", "grandparent", "sibling", "other"]),
            dict(n="isPrimary", k="bool", default=False, desc="A student has at most one primary guardian."),
            dict(n="isLegalCustody", k="bool", default=False),
            dict(n="canPickup", k="bool", default=True, desc="May collect the child from school."),
        ],
    ),
    dict(
        stem="addresses", nouns="student addresses", module="stu", name="StudentAddress", plural="StudentAddresses", path="/student-addresses", idParam="addressId",
        perm="stu.student_addresses", pkg="addresses", domain="Address", field="studentAddresses", noun="student address", in_use=False, audit_read=True,
        sort="studentName, kind, city, createdAt",
        filters=[dict(n="studentId", k="uuid"), dict(n="kind", k="enum", enum=["home", "mailing"])],
        fields=[
            dict(n="studentId", k="uuid", req=True),
            dict(n="registrationCode", k="string", ro=True),
            dict(n="studentName", k="string", ro=True),
            dict(n="kind", k="enum", req=True, enum=["home", "mailing"], desc="One of each kind per student."),
            dict(n="line1", k="string", req=True, max=200),
            dict(n="line2", k="string?", max=200),
            dict(n="city", k="string", req=True, max=100),
            dict(n="state", k="string?", max=100),
            dict(n="postcode", k="string?", max=10),
            dict(n="country", k="string", req=True, max=100, desc="Defaults to India on screens and in imports."),
        ],
    ),
]

RESOURCES += [
    dict(
        stem="enquiries", nouns="enquiries", module="adm", name="Enquiry", plural="Enquiries", path="/enquiries", idParam="enquiryId",
        perm="adm.enquiry", pkg="enquiries", domain="Enquiry", field="enquiries", noun="enquiry", in_use=False, audit_read=True,
        sort="code, childName, enquirerName, followUpDate, status, createdAt",
        filters=[
            dict(n="status", k="enum", enum=["new", "contacted", "visit_scheduled", "converted", "closed"]),
            dict(n="classId", k="uuid"),
            dict(n="source", k="enum", enum=["walk_in", "phone", "website", "referral", "social", "other"]),
            dict(n="followUpDue", k="bool"),
        ],
        fields=[
            dict(n="code", k="string", ro=True, desc="Numbered automatically (ENQ-000123) when the enquiry is created and never changed; unique and increasing in each institute. Enquiries from before automatic numbering keep the code the school typed."),
            dict(n="enquirerName", k="string", req=True, max=150, desc="The parent or guardian who enquired."),
            dict(n="phone", k="string", req=True, max=20, desc="7 to 20 digits; may include +, spaces, brackets or hyphens."),
            dict(n="email", k="string?", max=254),
            dict(n="childName", k="string", req=True, max=150),
            dict(n="dateOfBirth", k="date?", desc="Not in the future and not more than 30 years ago."),
            dict(n="classId", k="uuid?", desc="The class a place is sought in."),
            dict(n="classCode", k="string?", ro=True),
            dict(n="className", k="string?", ro=True),
            dict(n="yearId", k="uuid?", desc="The academic year a place is sought for."),
            dict(n="yearName", k="string?", ro=True),
            dict(n="source", k="enum", req=True, enum=["walk_in", "phone", "website", "referral", "social", "other"], desc="How the enquiry reached the school."),
            dict(n="status", k="enum", enum=["new", "contacted", "visit_scheduled", "converted", "closed"], default="new", desc="Converting to a registration comes with ADM-06; until then the status is set by hand."),
            dict(n="followUpDate", k="date?", desc="When to contact the family again."),
            dict(n="followUpDue", k="bool", ro=True, desc="True when the follow-up date is today or earlier (India time) and the enquiry is still new, contacted or visit_scheduled."),
            dict(n="notes", k="string?", max=1000),
            dict(n="assignedToId", k="uuid?", desc="The staff member following this enquiry up; not a parent or student account."),
            dict(n="assignedToName", k="string?", ro=True),
        ],
    ),
]

RESOURCES += [
    dict(
        stem="enquirydiscussions", nouns="enquiry discussions", module="adm", name="EnquiryDiscussion", plural="EnquiryDiscussions", path="/enquiry-discussions", idParam="discussionId",
        perm="adm.enquiry_discussions", pkg="enquirydiscussions", domain="Discussion", field="enquiryDiscussions", noun="enquiry discussion", in_use=False, audit_read=True,
        sort="contactedAt, enquiryCode, channel, outcome, nextFollowUpDate, createdAt",
        filters=[
            dict(n="enquiryId", k="uuid"),
            dict(n="channel", k="enum", enum=["call", "visit", "whatsapp", "email", "other"]),
            dict(n="outcome", k="enum", enum=["interested", "visit_scheduled", "call_back_later", "not_reachable", "not_interested", "other"]),
            dict(n="language", k="enum", enum=["en", "hi", "bn", "mr", "te", "ta", "gu", "ur", "kn", "ml", "or", "pa", "as", "other"]),
        ],
        fields=[
            dict(n="enquiryId", k="uuid", req=True, desc="The enquiry this contact was about."),
            dict(n="enquiryCode", k="string", ro=True),
            dict(n="enquiryChildName", k="string", ro=True),
            dict(n="contactedAt", k="datetime", req=True, desc="When the family was contacted (or contacted us). Not in the future."),
            dict(n="channel", k="enum", req=True, enum=["call", "visit", "whatsapp", "email", "other"]),
            dict(n="summary", k="string", req=True, max=200, desc="What was discussed, in a line."),
            dict(n="notes", k="string?", max=2000, desc="Fuller notes or a transcript."),
            dict(n="outcome", k="enum", req=True, enum=["interested", "visit_scheduled", "call_back_later", "not_reachable", "not_interested", "other"]),
            dict(n="nextFollowUpDate", k="date?", desc="When to contact the family next. Not before the day of this contact."),
            dict(n="updateEnquiryFollowUp", k="bool", wo=True, default=False, desc="When true, the enquiry's own follow-up date is set to nextFollowUpDate in the same save (nextFollowUpDate is then required)."),
            dict(n="language", k="enum", req=True, enum=["en", "hi", "bn", "mr", "te", "ta", "gu", "ur", "kn", "ml", "or", "pa", "as", "other"], desc="The language the conversation was held in (ISO 639-1 code, or other)."),
            dict(n="loggedById", k="uuid?", ro=True),
            dict(n="loggedByName", k="string?", ro=True),
        ],
    ),
]

RESOURCES += [
    dict(
        stem="registrations", nouns="student registrations", module="adm", name="StudentRegistration", plural="StudentRegistrations",
        path="/student-registrations", idParam="registrationId", perm="adm.student_registrations", pkg="registrations", domain="Registration",
        field="studentRegistrations", noun="student registration", in_use=False, audit_read=True, parity="Both",
        sort="applicationNumber, appliedOn, applicant, dateOfBirth, status, createdAt",
        filters=[
            dict(n="status", k="enum", enum=["submitted", "under_review", "accepted", "rejected", "withdrawn"]),
            dict(n="classId", k="uuid"),
            dict(n="yearId", k="uuid"),
        ],
        fields=[
            dict(n="applicationNumber", k="string", ro=True, desc="Given automatically, per institute (REG-000045); never changes and is not reused."),
            dict(n="appliedOn", k="date", req=True, desc="The day the family applied; not in the future (India time)."),
            dict(n="firstName", k="string", req=True, max=100),
            dict(n="middleName", k="string?", max=100),
            dict(n="lastName", k="string?", max=100),
            dict(n="fullName", k="string", ro=True),
            dict(n="dateOfBirth", k="date", req=True, desc="Not in the future and not more than 30 years ago."),
            dict(n="gender", k="string?", max=10, desc="male, female or other. May stay blank until admission."),
            dict(n="fatherName", k="string?", max=150),
            dict(n="motherName", k="string?", max=150),
            dict(n="guardianName", k="string?", max=150),
            dict(n="guardianPhone", k="string", req=True, max=20, desc="7 to 20 digits; may include +, spaces, brackets or hyphens."),
            dict(n="siblingStudentCode", k="string?", max=30, desc="The code of a brother or sister already studying here; it must match a student."),
            dict(n="classId", k="uuid", req=True, desc="The class a place is sought in."),
            dict(n="classCode", k="string", ro=True),
            dict(n="className", k="string", ro=True),
            dict(n="sectionId", k="uuid?", desc="Optional: allocated later. When given, it must be a section of this class in this academic year."),
            dict(n="sectionName", k="string?", ro=True),
            dict(n="yearId", k="uuid", req=True, desc="The academic year a place is sought for."),
            dict(n="yearName", k="string", ro=True),
            dict(n="status", k="enum", enum=["submitted", "under_review", "accepted", "rejected", "withdrawn"], default="submitted",
                 desc="Where the application stands. Becoming a student is a later step (admission, enrolment)."),
            dict(n="enquiryId", k="uuid?", ro=True, desc="The enquiry this registration was created from, if any."),
            dict(n="enquiryCode", k="string?", ro=True),
        ],
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
    for fname, ftype, extra in r.get("filters_contract", r["filters"]):
        schema = f"type: {ftype}" + (f", {extra}" if extra else "")
        filt += f"        - name: {fname}\n          in: query\n          schema: {{ {schema} }}\n"

    def errs(codes):
        names = {"400": "BadRequest", "401": "Unauthorized", "403": "Forbidden", "404": "NotFound", "409": "Conflict"}
        return "".join(f"        '{c}':\n          $ref: '{common}/responses/{names[c]}'\n" for c in codes)

    def hdr(op, summary, perm_action, extra=""):
        return (f"      operationId: {ops[op]}\n      summary: {summary}\n      tags: [{r['module']}]\n"
                f"      x-permission: {perm}.{perm_action}\n      x-module: {r['module']}\n      x-parity: {r.get('parity', 'Companion')}\n{extra}")

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

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/{r['module']}/{pkg}"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
)

var _ = resource.ErrNotFound

// {svc} is what the {r['noun']} operations need ({pkg}.Service satisfies it).
type {svc} interface {{
{('	ListAudited(ctx context.Context, actor audit.Actor, q crud.ListQuery, f ' + pkg + '.Filter) (crud.Page[' + pkg + '.' + dom + '], error)' + chr(10) + '	View(ctx context.Context, actor audit.Actor, id string) (' + pkg + '.' + dom + ', error)') if r.get('audit_read') else ('	List(ctx context.Context, q crud.ListQuery, f ' + pkg + '.Filter) (crud.Page[' + pkg + '.' + dom + '], error)' + chr(10) + '	Get(ctx context.Context, id string) (' + pkg + '.' + dom + ', error)')}
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
	p, err := h.{fld}.{"ListAudited(ctx, actorFrom(ctx), q, f)" if r.get("audit_read") else "List(ctx, q, f)"}
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
	it, err := h.{fld}.{"View(ctx, actorFrom(ctx), req." + idf + ".String())" if r.get("audit_read") else "Get(ctx, req." + idf + ".String())"}
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
        if "fields" in r:
            schemas(r)
            mp = ROOT / "internal/api/v1" / f"{r['stem']}_map_gen.go"
            mp.write_text(
                f"// Code generated by scripts/gen_resources.py from the {r['name']} definition; DO NOT EDIT.\n\npackage apiv1\n\n"
                f'import (\n\t"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"\n\t"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/{r["module"]}/{r["pkg"]}"\n\t"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"\n)\n'
                + maps(r)
            )
            go_files.append(str(mp))
        (ROOT / "api/openapi/modules" / f"{r['module']}-{r['stem']}.yaml").write_text(contract(r))
        path = ROOT / "internal/api/v1" / f"{r['stem']}_gen.go"
        path.write_text(handlers(r))
        go_files.append(str(path))
    # keep the output gofmt-clean so regenerating never produces noise in diffs
    subprocess.run(["gofmt", "-w", *go_files], check=True)
    print(f"generated {len(RESOURCES)} resources")


if __name__ == "__main__":
    main()
