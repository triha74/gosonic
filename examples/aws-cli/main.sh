#!/bin/bash
# This is a simple script that can be used to demonstrate AWS CLI commands
# You can run it directly with: ./main.sh

echo "AWS CLI Example"
echo "==============="
echo "To run with Gosonic:"
echo "export AWS_PROFILE=your-profile"
echo "export AWS_REGION=your-region"
echo "gosonic --var AWS_PROFILE=\${AWS_PROFILE} --var HOME=\${HOME}  run identity" 
echo "gosonic --var AWS_PROFILE=\${AWS_PROFILE} --var AWS_REGION=\${AWS_REGION} --var HOME=\${HOME} run s3-list"
echo "gosonic --var AWS_PROFILE=\${AWS_PROFILE} --var AWS_REGION=\${AWS_REGION} --var HOME=\${HOME} run ec2-describe"