-- Create a single schema for simplicity
CREATE SCHEMA IF NOT EXISTS ecommerce;

-- Set search path to the schema
SET search_path TO ecommerce;

-- Create products table
CREATE TABLE products (
    product_id BIGINT PRIMARY KEY,
    product_name VARCHAR(100) NOT NULL,
    unit_price DECIMAL(10, 2) NOT NULL CHECK (unit_price >= 0)
);

-- Create customers table
CREATE TABLE customers (
    customer_id BIGINT PRIMARY KEY,
    first_name VARCHAR(50) NOT NULL,
    email VARCHAR(100) NOT NULL UNIQUE
);

-- Create orders table with foreign key to customers
CREATE TABLE orders (
    order_id BIGINT PRIMARY KEY,
    customer_id BIGINT NOT NULL,
    order_date TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    total_amount DECIMAL(10, 2) NOT NULL CHECK (total_amount >= 0),
    CONSTRAINT fk_order_customer FOREIGN KEY (customer_id) REFERENCES customers (customer_id) ON DELETE RESTRICT
);

-- Create index on orders.customer_id for faster lookups
CREATE INDEX idx_orders_customer ON orders (customer_id);

-- Reset search path
SET search_path TO public;

CREATE TABLE testschema.test (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL
);
