CREATE SCHEMA IF NOT EXISTS testschema;
CREATE TABLE testschema.test (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL
);

CREATE TABLE testschema.test2 (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL
);

CREATE TABLE testschema.test3 (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    unit_price DECIMAL(10, 2) NOT NULL CHECK (unit_price >= 0)
);

Create index on testschema.test (name);

CREATE TABLE testschema.test4 (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    unit_price DECIMAL(10, 2) NOT NULL CHECK (unit_price >= 0)
);

