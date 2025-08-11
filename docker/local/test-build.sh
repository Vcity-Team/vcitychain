#!/bin/bash

echo "=== Testing VcityChain Build ==="

# Clean up any existing containers and images
echo "Cleaning up..."
docker-compose down
docker rmi local/vcitychain local/polygon-edge 2>/dev/null || true

# Build the image
echo "Building image..."
docker-compose -f docker-compose.test.yml build

# Check if build was successful
if [ $? -eq 0 ]; then
    echo "Build successful!"
    
    # List images
    echo "Available images:"
    docker images | grep -E "(vcitychain|polygon-edge)"
    
    # Test basic functionality
    echo "Testing basic functionality..."
    docker-compose -f docker-compose.test.yml up -d rootchain
    
    # Wait for rootchain to start
    sleep 5
    
    # Start init service
    docker-compose -f docker-compose.test.yml up -d init
    
    # Wait for init to complete
    sleep 10
    
    # Check container status
    echo "Container status:"
    docker-compose -f docker-compose.test.yml ps
    
    # Check logs
    echo "Init service logs:"
    docker-compose -f docker-compose.test.yml logs init
    
else
    echo "Build failed!"
    exit 1
fi
