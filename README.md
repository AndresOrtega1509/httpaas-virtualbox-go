# HTTPaaS — HTTP como servicio

HTTPaaS es una aplicación web desarrollada en **Go** que automatiza el aprovisionamiento de servidores HTTP en un entorno local virtualizado con **Oracle VirtualBox**, **Debian**, **Apache** y **Bind9**.

La aplicación permite crear y eliminar instancias a partir de una plantilla Debian con disco **Multiattach**, asignarles hostname e IP propios, registrar automáticamente sus nombres en el dominio local y desplegar contenido web suministrado por el usuario mediante archivos `.zip`.

## Funcionalidades

- Creación automática de máquinas virtuales mediante `VBoxManage`.
- Uso de una plantilla Debian con disco virtual en modo **Multiattach**.
- Configuración automática de hostname, red Host-Only e IP estática.
- Prevención de colisiones de IP consultando la zona Bind9 y las IPs reportadas por las VMs existentes.
- Verificación de la IP real de cada VM antes de publicar su registro DNS, usando Guest Additions y una comprobación IP/MAC desde el host como respaldo.
- Registro y eliminación automática de hosts en Bind9.
- Dashboard con hostname, IP, tipo de despliegue, estado y enlace de acceso.
- Eliminación automática de la VM y de su registro DNS.
- Persistencia local de las instancias administradas.
- Dos tipos de despliegue:
  - **Sitio estático** servido directamente por Apache.
  - **Aplicación HTTP ejecutable** gestionada por `systemd` y publicada mediante Apache reverse proxy.

## Arquitectura utilizada

La solución está diseñada para la siguiente infraestructura local:

- Red Host-Only: `192.168.56.0/24`.
- Adaptador Host-Only de Windows: `192.168.56.1`.
- Servidor DNS Bind9: `192.168.56.10`.
- Dominio local: `cloud.local`.
- Rango automático de instancias: `192.168.56.40` a `192.168.56.99`.
- Adaptador 1 de las VMs: NAT.
- Adaptador 2 de las VMs: Host-Only.

La VM `ns` administra la zona DNS. Las instancias HTTP son creadas a partir de la plantilla `ApacheBase` y utilizan un disco Multiattach para mantener una imagen base común y un disco diferencial independiente por instancia.

## Requisitos

### Equipo anfitrión

- Windows 64 bits.
- Oracle VirtualBox.
- `VBoxManage` disponible en:

```text
C:\Program Files\Oracle\VirtualBox\VBoxManage.exe
```

- Adaptador `VirtualBox Host-Only Ethernet Adapter` configurado en la red `192.168.56.0/24`.
- DNS del adaptador Host-Only configurado hacia `192.168.56.10`.

Go solo es necesario si se desea compilar o ejecutar el proyecto desde el código fuente. Se incluye `httpaas.exe` para Windows 64 bits.

### Servidor DNS `ns`

- Debian.
- Bind9 instalado y activo.
- Hostname: `ns`.
- IP Host-Only: `192.168.56.10`.
- Zona DNS: `/etc/bind/db.cloud.local`.
- VirtualBox Guest Additions instaladas.
- Usuario `andres` con permisos `sudo`.

### Plantilla `ApacheBase`

- Debian con Apache instalado.
- NetworkManager.
- `unzip`.
- VirtualBox Guest Additions.
- Usuario `andres` con permisos `sudo`.
- `enp0s3`: NAT.
- `enp0s8`: Host-Only.
- Disco base en modo Multiattach.
- Arranque EFI correctamente configurado.

Las rutas utilizadas por defecto son:

```text
C:\Users\ANDRES ORTEGA\VirtualBox VMs\ApacheBase\ApacheBase-disk1.vdi
C:\Users\ANDRES ORTEGA\VirtualBox VMs\ApacheBase\ApacheBase.nvram
```

Estas rutas pueden modificarse en `config.json`.

## Configuración

Los parámetros principales se encuentran en `config.json`:

```json
{
  "listen_addr": ":8080",
  "domain": "cloud.local",
  "ip_start": "192.168.56.40",
  "ip_end": "192.168.56.99",
  "dns_address": "192.168.56.10",
  "dns_vm_name": "ns"
}
```

También se configuran allí las rutas de VirtualBox, el disco Multiattach, el NVRAM de la plantilla, memoria, CPU y nombres de interfaces de red.

Las contraseñas no se almacenan en `config.json`. `run.ps1` las solicita al iniciar y las entrega temporalmente al proceso mediante variables de entorno.

## Ejecución

Desde PowerShell, ubicado en la carpeta del proyecto:

```powershell
powershell -ExecutionPolicy Bypass -File .\run.ps1
```

Luego abre en el navegador:

```text
http://localhost:8080
```

Opcionalmente puede ejecutarse un diagnóstico previo:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\check-environment.ps1
```

## Despliegue de un sitio estático

Selecciona **Sitio estático** en el dashboard.

El archivo `.zip` debe contener `index.html` directamente en la raíz, por ejemplo:

```text
sitio.zip
├── index.html
├── css/
├── js/
└── images/
```

HTTPaaS descomprime el contenido en `/var/www/html`, genera desde cero el VirtualHost de Apache y publica el sitio mediante:

```text
http://<hostname>.cloud.local
```

Se incluye `sample-site.zip` como ejemplo.

## Despliegue de una aplicación HTTP ejecutable

Selecciona **Aplicación HTTP ejecutable**.

El `.zip` debe contener en su raíz un binario Linux compatible con Debian/amd64 y todos los recursos que necesite. La aplicación debe iniciar un servidor HTTP en primer plano.

El formulario solicita:

- Nombre del ejecutable dentro del ZIP.
- Puerto HTTP interno.
- Argumentos de inicio opcionales.

Variables disponibles en los argumentos:

```text
{port}   → puerto interno seleccionado
{appdir} → /opt/httpaas/<hostname>
```

Ejemplo para el servidor de imágenes:

```text
servidor-imagenes.zip
├── srvimglinux
└── fotos/
    ├── imagen1.jpg
    ├── imagen2.jpg
    └── imagen3.jpg
```

Configuración en el dashboard:

```text
Ejecutable: srvimglinux
Puerto interno: 8000
Argumentos: {port} {appdir}/fotos
```

HTTPaaS realiza automáticamente:

1. Extracción del ZIP en `/opt/httpaas/<hostname>`.
2. Asignación de permisos de ejecución al binario.
3. Creación de un servicio `systemd` llamado `httpaas-<hostname>.service`.
4. Inicio y habilitación del servicio.
5. Habilitación de `proxy` y `proxy_http` en Apache.
6. Generación del VirtualHost de Apache como reverse proxy hacia el puerto interno.
7. Publicación mediante:

```text
http://<hostname>.cloud.local
```

Se incluye `sample-executable.zip` como aplicación ejecutable de ejemplo.

## Flujo de aprovisionamiento

Al solicitar una nueva instancia, HTTPaaS ejecuta de forma automática el siguiente proceso:

1. Valida el hostname y el paquete ZIP.
2. Consulta el estado actual de Bind9 y de las VMs registradas para reservar una IP realmente libre del rango configurado.
3. Crea y registra una VM en VirtualBox.
4. Configura EFI, memoria, CPU, NAT y Host-Only.
5. Conecta el disco Multiattach y genera un disco diferencial para la instancia.
6. Copia el NVRAM de la plantilla y arranca la VM.
7. Configura hostname, IP estática y DNS dentro de Debian.
8. Verifica que la VM haya tomado exactamente la IP reservada. Primero consulta Guest Additions y, si esa información de red no está disponible, valida desde Windows que la IP corresponda a la MAC Host-Only de la VM.
9. Despliega el contenido estático o la aplicación ejecutable.
10. Configura Apache según el tipo de despliegue.
11. Registra el hostname en Bind9 e incrementa el serial de la zona.
12. Muestra la instancia como activa en el dashboard.

## Eliminación de instancias

Al pulsar **Eliminar**, HTTPaaS:

1. Elimina el registro correspondiente de Bind9.
2. Incrementa y valida el serial de la zona DNS.
3. Detiene y elimina la VM de VirtualBox.
4. Elimina el disco diferencial administrado por VirtualBox.
5. Retira la instancia del dashboard.

## Archivos principales

```text
main.go                         Servidor web y endpoints
config.json                     Configuración de infraestructura
run.ps1                         Inicio seguro de la aplicación
internal/config/                Lectura de configuración
internal/model/                 Modelo de instancias
internal/provision/             Orquestación y despliegue
internal/store/                 Persistencia local
internal/vbox/                  Integración con VBoxManage
web/templates/                  Plantillas HTML
web/static/                     Estilos del dashboard
scripts/check-environment.ps1  Diagnóstico del entorno
sample-site.zip                 Ejemplo de sitio estático
sample-executable.zip           Ejemplo de aplicación ejecutable
httpaas.exe                     Ejecutable Windows 64 bits
```

## Consideraciones

HTTPaaS fue diseñado para un entorno académico local. Utiliza VirtualBox Guest Control y Guest Additions para automatizar operaciones dentro de las máquinas virtuales. Las credenciales se reciben al inicio y no se almacenan en el archivo de configuración.

Para un entorno de producción serían recomendables mecanismos adicionales como gestión de secretos, autenticación del dashboard, HTTPS, DNS dinámico mediante TSIG/`nsupdate`, monitoreo y control de recursos.
