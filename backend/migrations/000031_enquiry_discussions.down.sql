DROP TABLE IF EXISTS enquiry_discussions;
DROP TRIGGER IF EXISTS enquiries_assign_code ON enquiries;
DROP FUNCTION IF EXISTS enquiries_assign_code();
DROP TABLE IF EXISTS enquiry_counters;
COMMENT ON TABLE enquiries IS 'An admission enquiry (ADM-03). Personal data of a child and a parent: reads are audited. Codes are typed by the school. Status "converted" is set by hand until ADM-06 links an enquiry to a registration.';
