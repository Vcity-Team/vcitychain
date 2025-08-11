# Docker Compose for VcityChain

This directory contains Docker Compose configurations for running VcityChain nodes and related services.

## Files Overview

### 1. `docker-compose.yml` (Basic)
A simple configuration with just the main VcityChain service.

**Features:**
- Single VcityChain node
- Basic port mappings
- Volume persistence
- Network isolation

**Usage:**
```bash
# Start the service
docker-compose up -d

# View logs
docker-compose logs -f vcitychain

# Stop the service
docker-compose down
```

### 2. `docker-compose.full.yml` (Comprehensive)
A complete setup with multiple services for a full VcityChain network.

**Features:**
- Main VcityChain node
- Rootchain (Ethereum-compatible)
- Initialization service
- Multiple validator nodes
- Prometheus monitoring
- Grafana dashboard
- Complete network setup

**Usage:**
```bash
# Start all services
docker-compose -f docker-compose.full.yml up -d

# Start specific services
docker-compose -f docker-compose.full.yml up -d vcitychain rootchain

# View logs for specific service
docker-compose -f docker-compose.full.yml logs -f validator-1

# Stop all services
docker-compose -f docker-compose.full.yml down
```

## Port Mappings

| Service | Port | Purpose |
|---------|------|---------|
| VcityChain | 8545 | JSON-RPC HTTP |
| VcityChain | 8546 | JSON-RPC WebSocket |
| VcityChain | 9632 | gRPC |
| VcityChain | 1478 | LibP2P |
| VcityChain | 5001 | Prometheus metrics |
| Rootchain | 8545 | Ethereum JSON-RPC |
| Prometheus | 9090 | Monitoring UI |
| Grafana | 3000 | Dashboard UI |

## Environment Variables

You can customize the behavior by setting environment variables:

```bash
# Set consensus mechanism
export EDGE_CONSENSUS=polybft

# Set data directory
export DATA_DIR=/path/to/data
```

## Volume Mounts

- `vcitychain-data`: Main blockchain data
- `eth1data`: Rootchain data
- `prometheus-data`: Monitoring data
- `grafana-data`: Dashboard configurations

## Network Configuration

All services run on a custom bridge network `vcitychain-network` for isolation and communication.

## Building and Running

### Prerequisites
- Docker
- Docker Compose
- Go 1.21+ (for building)

### Build the Image
```bash
# Build using docker-compose
docker-compose build

# Or build manually
docker build -t vcitychain .
```

### Run Services
```bash
# Basic setup
docker-compose up -d

# Full setup
docker-compose -f docker-compose.full.yml up -d
```

## Monitoring

### Prometheus
- URL: http://localhost:9090
- Scrapes metrics from all VcityChain nodes
- Stores time-series data

### Grafana
- URL: http://localhost:3000
- Default credentials: admin/admin
- Pre-configured dashboards for VcityChain metrics

## Troubleshooting

### Common Issues

1. **Port Conflicts**
   - Ensure ports are not used by other services
   - Modify port mappings in docker-compose files

2. **Permission Issues**
   - Check volume mount permissions
   - Ensure Docker has access to host directories

3. **Service Dependencies**
   - Services start in order based on `depends_on`
   - Check logs for dependency failures

### Logs and Debugging
```bash
# View all logs
docker-compose logs

# View specific service logs
docker-compose logs vcitychain

# Follow logs in real-time
docker-compose logs -f

# Check service status
docker-compose ps
```

## Customization

### Adding New Services
```yaml
# Add to services section
new-service:
  image: your-image
  ports:
    - "8080:8080"
  networks:
    - vcitychain-network
```

### Modifying Commands
```yaml
# Override default command
command: ["server", "--config", "/app/config.yaml"]
```

### Environment Variables
```yaml
environment:
  - NODE_ENV=production
  - LOG_LEVEL=debug
```

## Security Considerations

- Services run in isolated networks
- Volumes are mounted as read-only where possible
- No sensitive data in environment variables
- Use secrets management for production

## Production Deployment

For production use:
1. Use external databases
2. Implement proper secrets management
3. Set up monitoring and alerting
4. Configure backup strategies
5. Use production-grade images
6. Implement health checks
7. Set resource limits

## Support

For issues and questions:
- Check the logs first
- Review the configuration
- Ensure all dependencies are met
- Check Docker and Docker Compose versions
