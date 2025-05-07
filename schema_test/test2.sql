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
    name VARCHAR(255) NOT NULL
);


