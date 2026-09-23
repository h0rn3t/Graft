CREATE TYPE billing.status AS ENUM ('open', 'paid');
CREATE TABLE billing.customers (id serial PRIMARY KEY, name text NOT NULL);
CREATE TABLE billing.invoices (
  id serial PRIMARY KEY,
  customer_id int REFERENCES billing.customers(id),
  state billing.status
);
CREATE VIEW billing.open_invoices AS
  SELECT i.id, c.name FROM billing.invoices i JOIN billing.customers c ON c.id = i.customer_id;
CREATE FUNCTION billing.total_due(cid int) RETURNS numeric AS $$
  SELECT count(*) FROM billing.open_invoices WHERE id = cid;
$$ LANGUAGE sql;
CREATE PROCEDURE billing.close_all() LANGUAGE sql AS $$ UPDATE billing.invoices SET state = 'paid'; $$;
