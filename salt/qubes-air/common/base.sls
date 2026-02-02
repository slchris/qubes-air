# Qubes Air Common Base State
#
# 所有 Qube 共享的基础配置

# ============================================
# 基础软件包
# ============================================

qubes-air-base-packages:
  pkg.installed:
    - pkgs:
      - curl
      - wget
      - jq
      - vim-enhanced
      - htop

# ============================================
# Qubes Air Agent 目录
# ============================================

/opt/qubes-air:
  file.directory:
    - user: root
    - group: root
    - mode: 755

/opt/qubes-air/bin:
  file.directory:
    - user: root
    - group: root
    - mode: 755
    - require:
      - file: /opt/qubes-air

/opt/qubes-air/etc:
  file.directory:
    - user: root
    - group: root
    - mode: 755
    - require:
      - file: /opt/qubes-air

/opt/qubes-air/log:
  file.directory:
    - user: root
    - group: root
    - mode: 755
    - require:
      - file: /opt/qubes-air

# ============================================
# Qubes Air 基础配置
# ============================================

/opt/qubes-air/etc/config.yaml:
  file.managed:
    - source: salt://qubes-air/common/files/config.yaml.j2
    - template: jinja
    - user: root
    - group: root
    - mode: 640
    - require:
      - file: /opt/qubes-air/etc

# ============================================
# 系统时间同步
# ============================================

chronyd:
  service.running:
    - enable: True
