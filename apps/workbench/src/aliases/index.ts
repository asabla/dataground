export {
  assignServiceAlias,
  isServiceAliasAssignmentScopeValid,
  isServiceAliasRoutedToRevision,
  readServiceAlias,
  type ServiceAliasAssignResult,
  type ServiceAliasFailure,
  type ServiceAliasMetadata,
  type ServiceAliasReadResult,
  type ServiceAliasReadScope,
  type ServiceAliasResource,
} from "./aliasClient";
export {
  createAliasIdempotencyKey,
  ServiceAliasAssignWorkflow,
  type ServiceAliasAssignWorkflowProps,
  validateServiceAliasName,
} from "./ServiceAliasAssignWorkflow";
