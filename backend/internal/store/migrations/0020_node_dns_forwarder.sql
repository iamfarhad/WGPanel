-- Node-local DNS for clients. A fixed public resolver in client configs (0018's
-- '1.1.1.1, 1.0.0.1') only works where the node's egress can reach it; where it
-- can't - filtered datacenters, corporate networks - clients connect but resolve
-- nothing. Agents now run a DNS forwarder on the node's tunnel address (the subnet's
-- .1) that relays to whatever resolver the server itself uses, and report it on each
-- heartbeat (dns_forwarder). client_dns = 'node' points clients at it; nodes whose
-- agent predates the forwarder fall back to the public default (see
-- httpapi.resolveClientDNS).
ALTER TABLE nodes ADD COLUMN dns_forwarder BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE panel_settings ALTER COLUMN client_dns SET DEFAULT 'node';
-- Only the untouched old default moves; an operator-chosen resolver is kept.
UPDATE panel_settings SET client_dns = 'node' WHERE client_dns = '1.1.1.1, 1.0.0.1';
