ALTER TABLE students DROP CONSTRAINT IF EXISTS students_photo_file_fk;
ALTER TABLE students DROP COLUMN IF EXISTS photo_file_id;
DROP TABLE IF EXISTS files;
