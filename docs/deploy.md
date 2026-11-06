# Despliegue continuo en AWS (ECR + ECS)

El workflow `.github/workflows/deploy.yml` **construye una vez y promueve la misma imagen**:

| Disparador | Qué hace |
|---|---|
| Push a `main` con el CI en verde | Publica `myapp-api`, `myapp-worker` y `myapp-migrate` en ECR con la etiqueta = SHA de 12 caracteres (con SBOM y procedencia adjuntos) y despliega en **QA** |
| *Actions → Deploy → Run workflow*, entorno `production`, etiqueta probada en QA | Despliega en **producción** esa misma imagen, sin reconstruir |
| *Run workflow*, entorno `qa`, etiqueta vacía | Publica el commit elegido, si falta, y lo despliega en QA. Si sus imágenes ya existen, las reutiliza: así se repite un despliegue o se reintenta una publicación a medias |

En cada despliegue, `scripts/deploy/ecs-deploy.sh` hace tres cosas:
1. **Migraciones:** las ejecuta como una task de un solo uso, con una espera de hasta 1 hora (`MIGRATION_TIMEOUT`). Si fallan o no terminan a tiempo, el despliegue se detiene y la versión anterior sigue sirviendo.
2. **Servicios:** actualiza la API y el worker con una revisión nueva de su task definition, en la que solo cambia la imagen. Sidecars, variables y secretos se conservan.
3. **Estabilidad:** espera a que la versión nueva quede `COMPLETED` en cada servicio (hasta 30 minutos, `DEPLOY_TIMEOUT`).

El rollback automático lo hace ECS con el *deployment circuit breaker*. **Si ECS revierte, el pipeline falla:** la etiqueta no quedó desplegada y no debe promoverse. Esta comprobación usa `rolloutState`, que ECS solo informa en servicios con *rolling update* que no estén detrás de un Classic Load Balancer.

Mientras no estén configuradas las variables de abajo, el workflow se omite sin fallar.

---

## 1. Infraestructura que el pipeline espera

### ECR
Tres repositorios: `myapp-api`, `myapp-worker` y `myapp-migrate` (`IMAGE_PREFIX` cambia el prefijo). Configuración de cada uno:
- **Tag immutability: ON.** Una etiqueta nunca cambia de contenido, y promover a producción es desplegar exactamente lo que se probó.
- **Scan on push** (Enhanced scanning de Inspector): un segundo control, además del Trivy del CI.
- Una lifecycle policy, para no acumular imágenes viejas indefinidamente.

### ECS (Fargate), por entorno
- **Servicio de la API** con:
  - el contenedor `api`;
  - el sidecar `adot`, si se usa OpenTelemetry (`deploy/otel/adot-collector.yaml`), con la imagen que fija `production.yml`, que es la versión probada con esa configuración (`deploy/otel/adot-collector_test.sh`);
  - el health check del target group del ALB en `/health/ready`;
  - el **deployment circuit breaker con rollback**;
  - `stopTimeout` ≥ 45 s, mayor que `HTTP_DRAIN_DELAY` + `HTTP_SHUTDOWN_TIMEOUT`.
- **Servicio del worker** con:
  - el contenedor `worker`;
  - `stopTimeout` ≥ 75 s, mayor que `WORKER_SHUTDOWN_TIMEOUT` y que `SQS_VISIBILITY_TIMEOUT`. ECS admite como máximo 120 s;
  - el **health check del contenedor declarado en la task definition**, porque ECS ignora el `HEALTHCHECK` del Dockerfile: `"healthCheck": {"command": ["CMD", "/app/worker", "healthcheck"], "interval": 30, "timeout": 5, "retries": 3, "startPeriod": 30}`. Para la API: `["CMD", "/app/api", "healthcheck"]`.
- **Task definition de migraciones** (sin servicio), con el contenedor `migrate` y el comando `up`.
- Las variables de entorno y los secretos (`DB_PASSWORD`, `JWT_SECRET`…) van en las task definitions, desde Secrets Manager o SSM. El pipeline nunca los ve.

### OIDC y roles de IAM
Un proveedor OIDC de identidad `token.actions.githubusercontent.com` (audiencia `sts.amazonaws.com`) y **dos roles**, cada uno con lo mínimo.

**Primero, personalizar el `sub` del token para que incluya la rama.** Una ejecución manual puede lanzarse desde cualquier rama, con el workflow y el script de esa rama. Con el `sub` por defecto, un job del Environment `production` recibe `repo:…:environment:production` aunque venga de una rama sin revisar. Una comprobación dentro del workflow no protege, porque la rama puede quitarla. La barrera tiene que estar en IAM:

```bash
gh api -X PUT repos/PySlim/plantilla_backend_go/actions/oidc/customization/sub \
  -F use_default=false -f 'include_claim_keys[]=repo' -f 'include_claim_keys[]=context' -f 'include_claim_keys[]=ref'
```

Con eso, el `sub` queda así:
- un job de Environment: `repo:PySlim/plantilla_backend_go:environment:production:ref:refs/heads/main`;
- el job `publish`, que no usa Environment: `repo:PySlim/plantilla_backend_go:ref:refs/heads/main:ref:refs/heads/main`.

Las políticas de confianza de abajo exigen `refs/heads/main`. Si la primera ejecución falla con `Not authorized to perform sts:AssumeRoleWithWebIdentity`, compara el `sub` real (en CloudTrail, evento `AssumeRoleWithWebIdentity`) con el de la política.

**`github-ecr-push`**: publicar imágenes. Lo asume el job `publish`, que corre en `main`.

```json
{
  "Effect": "Allow",
  "Principal": { "Federated": "arn:aws:iam::<ACCOUNT>:oidc-provider/token.actions.githubusercontent.com" },
  "Action": "sts:AssumeRoleWithWebIdentity",
  "Condition": {
    "StringEquals": {
      "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
      "token.actions.githubusercontent.com:sub": "repo:PySlim/plantilla_backend_go:ref:refs/heads/main:ref:refs/heads/main"
    }
  }
}
```

Permisos:
- `ecr:GetAuthorizationToken` sobre `*`.
- Sobre los tres repositorios: `ecr:BatchCheckLayerAvailability`, `ecr:InitiateLayerUpload`, `ecr:UploadLayerPart`, `ecr:CompleteLayerUpload`, `ecr:PutImage`, `ecr:BatchGetImage` y `ecr:DescribeImages`.

**`github-ecs-deploy-<entorno>`**: desplegar. Hay uno por entorno, y cada uno lo asume solo el job de **su** GitHub Environment:

```json
"token.actions.githubusercontent.com:sub": "repo:PySlim/plantilla_backend_go:environment:production:ref:refs/heads/main"
```

Permisos:
- `ecs:DescribeTaskDefinition` y `ecs:RegisterTaskDefinition` (sobre `*`).
- `ecs:RunTask` sobre la familia de migraciones.
- `ecs:DescribeTasks`, `ecs:DescribeServices` y `ecs:UpdateService` sobre los servicios del entorno.
- `ecr:DescribeImages` sobre los tres repositorios.
- `iam:PassRole` sobre el task role y el execution role del entorno, con la condición `iam:PassedToService = ecs-tasks.amazonaws.com`.

Así, ni un job de QA ni uno lanzado desde una rama que no sea `main` puede asumir el rol de producción. La condición `sub` lo impide aunque alguien modifique el workflow en su rama.

---

## 2. Configuración en GitHub

**Variables del repositorio** (*Settings → Secrets and variables → Actions → Variables*):

| Variable | Ejemplo |
|---|---|
| `AWS_REGION` | `us-east-1` |
| `ECR_REGISTRY` | `123456789012.dkr.ecr.us-east-1.amazonaws.com` |
| `AWS_ECR_ROLE_ARN` | `arn:aws:iam::123456789012:role/github-ecr-push` |
| `IMAGE_PREFIX` | Opcional: prefijo de los repositorios de ECR. Por defecto, `myapp` |

**Environments `qa` y `production`**, cada uno con sus variables:

| Variable | Ejemplo |
|---|---|
| `AWS_DEPLOY_ROLE_ARN` | `arn:aws:iam::123456789012:role/github-ecs-deploy-qa` |
| `ECS_CLUSTER` | `myapp-qa` |
| `ECS_SERVICE_API` | `myapp-api` |
| `ECS_SERVICE_WORKER` | `myapp-worker` (vacía si el entorno no tiene worker) |
| `MIGRATE_TASK_FAMILY` | `myapp-migrate` |
| `SUBNETS` | `subnet-0a…,subnet-0b…` (privadas, con salida a ECR y a la base) |
| `SECURITY_GROUPS` | `sg-0c…` (con acceso a PostgreSQL) |
| `CONTAINER_API`, `CONTAINER_WORKER`, `CONTAINER_MIGRATE` | Opcionales: nombres de los contenedores en las task definitions. Por defecto, `api`, `worker` y `migrate` |

No hace falta ningún *secret*: con OIDC no hay claves de AWS que guardar.

**Aprobación de producción.** Hoy la aprobación es la ejecución manual del workflow. En un repositorio privado del plan gratuito, GitHub no permite *required reviewers* en los Environments. Con GitHub Pro, Team o un repositorio público, conviene agregarlos al Environment `production`: así, además de ejecutarse a mano, el despliegue espera la aprobación de una persona.

---

## 3. Operación

- **Promover a producción:** *Actions → Deploy → Run workflow*, `environment: production`, `tag:` la etiqueta del despliegue de QA, que aparece en el resumen del job y en ECR.
- **Volver a una versión anterior:** el mismo procedimiento, con una etiqueta anterior y la casilla `rollback` marcada, que no ejecuta migraciones. Sin ella, si la versión de la que se vuelve agregó una migración, el despliegue se detiene: la imagen anterior no la conoce (`no migration found for version N`). Las migraciones son *forward-only*: el esquema no retrocede, y una versión anterior debe tolerar el esquema nuevo. Por eso un cambio incompatible del esquema se hace en dos despliegues: primero se agrega, después se quita lo viejo.
- **Si fallan las migraciones:** los servicios no se tocan. El motivo está en los logs de la task de migraciones, en CloudWatch Logs.
- **SBOM y procedencia** de una imagen publicada: `docker buildx imagetools inspect <registry>/myapp-api:<tag> --format '{{ json .SBOM }}'`.
