export {
  assignServiceAlias,
  isServiceAliasAssignmentScopeValid,
  isServiceAliasRoutedToRevision,
  type ServiceAliasAssignResult,
  type ServiceAliasFailure,
  type ServiceAliasMetadata,
  type ServiceAliasResource,
} from "./aliasClient";
export {
  createAliasIdempotencyKey,
  ServiceAliasAssignWorkflow,
  type ServiceAliasAssignWorkflowProps,
  validateServiceAliasName,
} from "./ServiceAliasAssignWorkflow";
