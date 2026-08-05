-- dataground:up

ALTER TABLE audit_export_delivery_transports
    DROP CONSTRAINT audit_export_delivery_transports_transport_contract_check,
    ADD CONSTRAINT audit_export_delivery_transports_transport_contract_check
        CHECK (transport_contract IN (
            'dataground.audit-export-transport/s3-immutable/v1',
            'dataground.audit-export-transport/s3-immutable-mtls/v2'
        ));

-- dataground:down

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM audit_export_delivery_transports
        WHERE transport_contract = 'dataground.audit-export-transport/s3-immutable-mtls/v2'
    ) THEN
        RAISE EXCEPTION 'schema 29 contains authenticated audit transport evidence and cannot be downgraded safely';
    END IF;
END;
$$;

ALTER TABLE audit_export_delivery_transports
    DROP CONSTRAINT audit_export_delivery_transports_transport_contract_check,
    ADD CONSTRAINT audit_export_delivery_transports_transport_contract_check
        CHECK (transport_contract = 'dataground.audit-export-transport/s3-immutable/v1');
