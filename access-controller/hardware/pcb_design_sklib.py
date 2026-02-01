from collections import defaultdict
from skidl import Pin, Part, Alias, SchLib, SKIDL, TEMPLATE

from skidl.pin import pin_types

SKIDL_lib_version = '0.0.1'

pcb_design = SchLib(tool=SKIDL).add_parts(*[
        Part(**{ 'name':'Conn_01x02_Pin', 'dest':TEMPLATE, 'tool':SKIDL, 'aliases':Alias({'Conn_01x02_Pin'}), 'ref_prefix':'J', 'fplist':[''], 'footprint':'TerminalBlock_Phoenix:TerminalBlock_Phoenix_MKDS-1,5-2_1x02_P5.00mm_Horizontal', 'keywords':'connector', 'description':'Generic connector, single row, 01x02, script generated', 'datasheet':'~', 'pins':[
            Pin(num='1',name='Pin_1',func=pin_types.PASSIVE,unit=1),
            Pin(num='2',name='Pin_2',func=pin_types.PASSIVE,unit=1)] }),
        Part(**{ 'name':'Conn_01x04_Pin', 'dest':TEMPLATE, 'tool':SKIDL, 'aliases':Alias({'Conn_01x04_Pin'}), 'ref_prefix':'J', 'fplist':[''], 'footprint':'TerminalBlock_Phoenix:TerminalBlock_Phoenix_MKDS-1,5-4_1x04_P5.00mm_Horizontal', 'keywords':'connector', 'description':'Generic connector, single row, 01x04, script generated', 'datasheet':'~', 'pins':[
            Pin(num='1',name='Pin_1',func=pin_types.PASSIVE,unit=1),
            Pin(num='2',name='Pin_2',func=pin_types.PASSIVE,unit=1),
            Pin(num='3',name='Pin_3',func=pin_types.PASSIVE,unit=1),
            Pin(num='4',name='Pin_4',func=pin_types.PASSIVE,unit=1)] }),
        Part(**{ 'name':'D', 'dest':TEMPLATE, 'tool':SKIDL, 'aliases':Alias({'D'}), 'ref_prefix':'D', 'fplist':[''], 'footprint':'Diode_THT:D_DO-201_P15.24mm_Horizontal', 'keywords':'diode', 'description':'Diode', 'datasheet':'~', 'pins':[
            Pin(num='1',name='K',func=pin_types.PASSIVE,unit=1),
            Pin(num='2',name='A',func=pin_types.PASSIVE,unit=1)] }),
        Part(**{ 'name':'C_Polarized', 'dest':TEMPLATE, 'tool':SKIDL, 'aliases':Alias({'C_Polarized'}), 'ref_prefix':'C', 'fplist':[''], 'footprint':'Capacitor_THT:CP_Radial_D6.3mm_P2.50mm', 'keywords':'cap capacitor', 'description':'Polarized capacitor', 'datasheet':'~', 'pins':[
            Pin(num='1',name='~',func=pin_types.PASSIVE,unit=1),
            Pin(num='2',name='~',func=pin_types.PASSIVE,unit=1)] }),
        Part(**{ 'name':'C', 'dest':TEMPLATE, 'tool':SKIDL, 'aliases':Alias({'C'}), 'ref_prefix':'C', 'fplist':[''], 'footprint':'Capacitor_SMD:C_0805_2012Metric', 'keywords':'cap capacitor', 'description':'Unpolarized capacitor', 'datasheet':'~', 'pins':[
            Pin(num='1',name='~',func=pin_types.PASSIVE,unit=1),
            Pin(num='2',name='~',func=pin_types.PASSIVE,unit=1)] }),
        Part(**{ 'name':'AMS1117-3.3', 'dest':TEMPLATE, 'tool':SKIDL, 'aliases':Alias({'AMS1117-3.3'}), 'ref_prefix':'U', 'fplist':['Package_TO_SOT_SMD:SOT-223-3_TabPin2', 'Package_TO_SOT_SMD:SOT-223-3_TabPin2'], 'footprint':'Package_TO_SOT_SMD:SOT-223-3_TabPin2', 'keywords':'linear regulator ldo fixed positive', 'description':'1A Low Dropout regulator, positive, 3.3V fixed output, SOT-223', 'datasheet':'http://www.advanced-monolithic.com/pdf/ds1117.pdf', 'pins':[
            Pin(num='3',name='VI',func=pin_types.PWRIN,unit=1),
            Pin(num='1',name='GND',func=pin_types.PWRIN,unit=1),
            Pin(num='2',name='VO',func=pin_types.PWROUT,unit=1)] }),
        Part(**{ 'name':'Conn_01x19_Socket', 'dest':TEMPLATE, 'tool':SKIDL, 'aliases':Alias({'Conn_01x19_Socket'}), 'ref_prefix':'J', 'fplist':[''], 'footprint':'Connector_PinSocket_2.54mm:PinSocket_1x19_P2.54mm_Vertical', 'keywords':'connector', 'description':'Generic connector, single row, 01x19, script generated', 'datasheet':'~', 'pins':[
            Pin(num='1',name='Pin_1',func=pin_types.PASSIVE,unit=1),
            Pin(num='2',name='Pin_2',func=pin_types.PASSIVE,unit=1),
            Pin(num='3',name='Pin_3',func=pin_types.PASSIVE,unit=1),
            Pin(num='4',name='Pin_4',func=pin_types.PASSIVE,unit=1),
            Pin(num='5',name='Pin_5',func=pin_types.PASSIVE,unit=1),
            Pin(num='6',name='Pin_6',func=pin_types.PASSIVE,unit=1),
            Pin(num='7',name='Pin_7',func=pin_types.PASSIVE,unit=1),
            Pin(num='8',name='Pin_8',func=pin_types.PASSIVE,unit=1),
            Pin(num='9',name='Pin_9',func=pin_types.PASSIVE,unit=1),
            Pin(num='10',name='Pin_10',func=pin_types.PASSIVE,unit=1),
            Pin(num='11',name='Pin_11',func=pin_types.PASSIVE,unit=1),
            Pin(num='12',name='Pin_12',func=pin_types.PASSIVE,unit=1),
            Pin(num='13',name='Pin_13',func=pin_types.PASSIVE,unit=1),
            Pin(num='14',name='Pin_14',func=pin_types.PASSIVE,unit=1),
            Pin(num='15',name='Pin_15',func=pin_types.PASSIVE,unit=1),
            Pin(num='16',name='Pin_16',func=pin_types.PASSIVE,unit=1),
            Pin(num='17',name='Pin_17',func=pin_types.PASSIVE,unit=1),
            Pin(num='18',name='Pin_18',func=pin_types.PASSIVE,unit=1),
            Pin(num='19',name='Pin_19',func=pin_types.PASSIVE,unit=1)] }),
        Part(**{ 'name':'PC817', 'dest':TEMPLATE, 'tool':SKIDL, 'aliases':Alias({'PC817'}), 'ref_prefix':'U', 'fplist':['Package_DIP:DIP-4_W7.62mm'], 'footprint':'Package_DIP:DIP-4_W7.62mm', 'keywords':'NPN DC Optocoupler', 'description':'DC Optocoupler, Vce 35V, CTR 50-300%, DIP-4', 'datasheet':'http://www.soselectronic.cz/a_info/resource/d/pc817.pdf', 'pins':[
            Pin(num='1',name='~',func=pin_types.PASSIVE,unit=1),
            Pin(num='2',name='~',func=pin_types.PASSIVE,unit=1),
            Pin(num='4',name='~',func=pin_types.PASSIVE,unit=1),
            Pin(num='3',name='~',func=pin_types.PASSIVE,unit=1)] }),
        Part(**{ 'name':'R', 'dest':TEMPLATE, 'tool':SKIDL, 'aliases':Alias({'R'}), 'ref_prefix':'R', 'fplist':[''], 'footprint':'Resistor_SMD:R_0805_2012Metric', 'keywords':'R res resistor', 'description':'Resistor', 'datasheet':'~', 'pins':[
            Pin(num='1',name='~',func=pin_types.PASSIVE,unit=1),
            Pin(num='2',name='~',func=pin_types.PASSIVE,unit=1)] }),
        Part(**{ 'name':'PN2222A', 'dest':TEMPLATE, 'tool':SKIDL, 'aliases':Alias({'PN2222A'}), 'ref_prefix':'Q', 'fplist':['', 'Package_TO_SOT_THT:TO-92_Inline'], 'footprint':'Package_TO_SOT_THT:TO-92_Inline', 'keywords':'NPN Transistor', 'description':'1A Ic, 40V Vce, NPN Transistor, General Purpose Transistor, TO-92', 'datasheet':'https://www.onsemi.com/pub/Collateral/PN2222-D.PDF', 'pins':[
            Pin(num='2',name='B',func=pin_types.INPUT,unit=1),
            Pin(num='3',name='C',func=pin_types.PASSIVE,unit=1),
            Pin(num='1',name='E',func=pin_types.PASSIVE,unit=1)] })])