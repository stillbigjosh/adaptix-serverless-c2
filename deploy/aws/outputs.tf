output "relay_url" {
  description = "Lambda function URL for agent callbacks"
  value       = aws_lambda_function_url.relay_url.function_url
}

output "agents_table_arn" {
  description = "ARN of the agents DynamoDB table"
  value       = aws_dynamodb_table.agents.arn
}

output "inbound_table_arn" {
  description = "ARN of the inbound DynamoDB table"
  value       = aws_dynamodb_table.inbound.arn
}

output "outbound_table_arn" {
  description = "ARN of the outbound DynamoDB table"
  value       = aws_dynamodb_table.outbound.arn
}

output "tasklog_table_arn" {
  description = "ARN of the tasklog DynamoDB table"
  value       = aws_dynamodb_table.tasklog.arn
}
