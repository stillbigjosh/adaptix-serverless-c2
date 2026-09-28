variable "region" {
  description = "AWS region"
  type        = string
  default     = "us-east-1"
}

variable "function_name" {
  description = "Lambda function name"
  type        = string
  default     = "adaptix-relay"
}

variable "agents_table" {
  description = "DynamoDB table name for agents"
  type        = string
  default     = "adaptix-agents"
}

variable "inbound_table" {
  description = "DynamoDB table name for inbound data"
  type        = string
  default     = "adaptix-inbound"
}

variable "outbound_table" {
  description = "DynamoDB table name for outbound data"
  type        = string
  default     = "adaptix-outbound"
}

variable "tasklog_table" {
  description = "DynamoDB table name for task logs"
  type        = string
  default     = "adaptix-tasklog"
}

variable "relay_api_key" {
  description = "API key for relay function authentication"
  type        = string
  sensitive   = true
  default     = ""
}

variable "tags" {
  description = "Tags to apply to all resources"
  type        = map(string)
  default     = {}
}
