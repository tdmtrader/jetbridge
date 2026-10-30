-- The Let's Encrypt/ACME listener and its autocert cache are gone; nothing
-- reads or writes cert_cache any more. The web serves TLS only from
-- --tls-cert/--tls-key.
DROP TABLE cert_cache;
