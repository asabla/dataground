-- dataground:up

ALTER TABLE invocation_authorization_policies
    DROP CONSTRAINT invocation_authorization_policies_contract_check,
    ADD COLUMN cedar_entities bytea,
    ADD CONSTRAINT invocation_authorization_policies_contract_check
        CHECK (contract IN (
            'dataground.invocation-authorization-policy/v1',
            'dataground.invocation-authorization-policy/v2'
        )),
    ADD CONSTRAINT invocation_authorization_policies_entities_check
        CHECK (
            (contract = 'dataground.invocation-authorization-policy/v1' AND cedar_entities IS NULL)
            OR
            (contract = 'dataground.invocation-authorization-policy/v2'
                AND octet_length(cedar_entities) BETWEEN 1 AND 1048576)
        );

-- dataground:down

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM invocation_authorization_policies
        WHERE contract = 'dataground.invocation-authorization-policy/v2'
    ) THEN
        RAISE EXCEPTION 'cannot remove invocation authorization entity evidence';
    END IF;
END;
$$;

ALTER TABLE invocation_authorization_policies
    DROP CONSTRAINT invocation_authorization_policies_entities_check,
    DROP CONSTRAINT invocation_authorization_policies_contract_check,
    DROP COLUMN cedar_entities,
    ADD CONSTRAINT invocation_authorization_policies_contract_check
        CHECK (contract = 'dataground.invocation-authorization-policy/v1');
