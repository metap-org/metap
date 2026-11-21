-- A "userpass" credential (a username and a password): the username is not secret and is shown;
-- the password lives with the other credential values (the metap database sealed, or the secret
-- manager), together with the username, under the same derived reference.
ALTER TABLE metadata.tenant_credentials ADD COLUMN username text NOT NULL DEFAULT '';
