terraform {
  required_version = ">= 1.5.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.0"
    }
  }
}

provider "aws" {
  region = var.region
}

# --- DynamoDB Tables ---

resource "aws_dynamodb_table" "agents" {
  name         = var.agents_table
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "agent_id"

  attribute {
    name = "agent_id"
    type = "S"
  }

  ttl {
    attribute_name = "ttl"
    enabled        = true
  }

  tags = var.tags
}

resource "aws_dynamodb_table" "inbound" {
  name         = var.inbound_table
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }

  ttl {
    attribute_name = "ttl"
    enabled        = true
  }

  tags = var.tags
}

resource "aws_dynamodb_table" "outbound" {
  name         = var.outbound_table
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }

  attribute {
    name = "agent_id"
    type = "S"
  }

  global_secondary_index {
    name            = "agent_id-index"
    hash_key        = "agent_id"
    projection_type = "ALL"
  }

  ttl {
    attribute_name = "ttl"
    enabled        = true
  }

  tags = var.tags
}

resource "aws_dynamodb_table" "tasklog" {
  name         = var.tasklog_table
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }

  ttl {
    attribute_name = "ttl"
    enabled        = true
  }

  tags = var.tags
}

# --- IAM ---

resource "aws_iam_role" "lambda_role" {
  name = "${var.function_name}-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action = "sts:AssumeRole"
      Effect = "Allow"
      Principal = {
        Service = "lambda.amazonaws.com"
      }
    }]
  })

  tags = var.tags
}

resource "aws_iam_role_policy" "lambda_dynamo" {
  name = "${var.function_name}-dynamo"
  role = aws_iam_role.lambda_role.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = [
          "dynamodb:PutItem",
          "dynamodb:GetItem",
          "dynamodb:UpdateItem",
          "dynamodb:Query"
        ]
        Resource = [
          aws_dynamodb_table.inbound.arn,
          aws_dynamodb_table.outbound.arn,
          "${aws_dynamodb_table.outbound.arn}/index/*"
        ]
      },
      {
        Effect = "Allow"
        Action = [
          "logs:CreateLogGroup",
          "logs:CreateLogStream",
          "logs:PutLogEvents"
        ]
        Resource = "arn:aws:logs:*:*:log-group:/aws/lambda/${var.function_name}:*"
      },
      {
        Effect = "Allow"
        Action = [
          "kms:Decrypt",
          "kms:GenerateDataKey"
        ]
        Resource = aws_kms_key.adaptix.arn
      }
    ]
  })
}

# --- Lambda ---

resource "null_resource" "build_lambda" {
  triggers = {
    source_hash = filesha256("${path.module}/lambda/main.go")
  }

  provisioner "local-exec" {
    command = <<-EOT
      cd ${path.module}/lambda && \
      GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bootstrap main.go && \
      zip -j ${path.module}/lambda.zip bootstrap && \
      rm bootstrap
    EOT
  }
}

data "archive_file" "lambda_dummy" {
  type        = "zip"
  output_path = "${path.module}/lambda_dummy.zip"

  source {
    content  = "placeholder"
    filename = "placeholder"
  }
}

resource "aws_lambda_function" "relay" {
  function_name = var.function_name
  role          = aws_iam_role.lambda_role.arn
  handler       = "bootstrap"
  runtime       = "provided.al2023"
  architectures = ["x86_64"]
  timeout       = 30
  memory_size   = 128

  filename         = fileexists("${path.module}/lambda.zip") ? "${path.module}/lambda.zip" : data.archive_file.lambda_dummy.output_path
  source_code_hash = fileexists("${path.module}/lambda.zip") ? filebase64sha256("${path.module}/lambda.zip") : data.archive_file.lambda_dummy.output_base64sha256

  environment {
    variables = {
      INBOUND_TABLE  = var.inbound_table
      OUTBOUND_TABLE = var.outbound_table
      RELAY_API_KEY  = var.relay_api_key
    }
  }

  tags = var.tags

  depends_on = [null_resource.build_lambda]
}

resource "aws_lambda_function_url" "relay_url" {
  function_name      = aws_lambda_function.relay.function_name
  authorization_type = "NONE"
}

resource "aws_lambda_permission" "public_invoke_url" {
  statement_id           = "AllowPublicAccessUrl"
  action                 = "lambda:InvokeFunctionUrl"
  function_name          = aws_lambda_function.relay.function_name
  principal              = "*"
  function_url_auth_type = "NONE"
}

resource "aws_lambda_permission" "public_invoke_function" {
  statement_id  = "AllowPublicAccessFunction"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.relay.function_name
  principal     = "*"
}

# --- KMS ---

data "aws_caller_identity" "current" {}

resource "aws_kms_key" "adaptix" {
  description             = "Adaptix Serverless C2 encryption key"
  deletion_window_in_days = 7
  enable_key_rotation     = true

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "EnableRootAccount"
        Effect    = "Allow"
        Principal = { AWS = "arn:aws:iam::${data.aws_caller_identity.current.account_id}:root" }
        Action    = "kms:*"
        Resource  = "*"
      },
      {
        Sid    = "AllowCloudWatchLogs"
        Effect = "Allow"
        Principal = { Service = "logs.${var.region}.amazonaws.com" }
        Action = [
          "kms:Encrypt",
          "kms:Decrypt",
          "kms:GenerateDataKey*",
          "kms:DescribeKey"
        ]
        Resource = "*"
        Condition = {
          ArnLike = {
            "kms:EncryptionContext:aws:logs:arn" = "arn:aws:logs:${var.region}:${data.aws_caller_identity.current.account_id}:log-group:/aws/lambda/${var.function_name}"
          }
        }
      }
    ]
  })

  tags = var.tags
}

resource "aws_kms_alias" "adaptix" {
  name          = "alias/${var.function_name}"
  target_key_id = aws_kms_key.adaptix.key_id
}

# --- CloudWatch ---

resource "aws_cloudwatch_log_group" "lambda_logs" {
  name              = "/aws/lambda/${var.function_name}"
  retention_in_days = 3
  kms_key_id        = aws_kms_key.adaptix.arn

  tags = var.tags
}
