resource "aws_instance" "app" {
  ami                    = data.aws_ami.al2023.id
  instance_type          = var.instance_type
  subnet_id              = aws_subnet.public.id
  vpc_security_group_ids = [aws_security_group.ec2.id]
  associate_public_ip_address = true
  iam_instance_profile   = aws_iam_instance_profile.ec2.name

  user_data_base64 = base64encode(templatefile("${path.module}/user_data.sh.tftpl", {
    aws_region         = var.aws_region
    ecr_repo_url       = aws_ecr_repository.app.repository_url
    image_tag          = var.app_image_tag
    ssm_prefix         = var.ssm_prefix
    tenants_table      = local.tenants_table
    usage_table        = local.usage_table
    anomalies_table    = local.anomalies_table
    cookie_secure      = local.cookie_secure
    caddy_domain       = local.caddy_domain
  }))

  root_block_device {
    volume_size = 10
    volume_type = "gp3"
    encrypted   = true
  }

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }

  tags = { Name = "${local.name}-app" }
}
