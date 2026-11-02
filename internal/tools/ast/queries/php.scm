; Exported API of PHP code: functions, classes, interfaces, traits, enums
; and their public methods. Names are namespace-qualified in code from the
; @namespace captures. Private/protected methods are filtered in code
; (visibility_modifier).

(namespace_definition name: (namespace_name) @namespace)

(function_definition
  name: (name) @name
  parameters: (formal_parameters) @sig
  return_type: (_)? @sig) @def.function

(class_declaration name: (name) @name) @def.class
(interface_declaration name: (name) @name) @def.interface
(trait_declaration name: (name) @name) @def.trait
(enum_declaration name: (name) @name) @def.enum

(class_declaration
  name: (name) @scope
  body: (declaration_list (method_declaration
    name: (name) @name
    parameters: (formal_parameters) @sig
    return_type: (_)? @sig) @def.method))

(interface_declaration
  name: (name) @scope
  body: (declaration_list (method_declaration
    name: (name) @name
    parameters: (formal_parameters) @sig
    return_type: (_)? @sig) @def.method))

(trait_declaration
  name: (name) @scope
  body: (declaration_list (method_declaration
    name: (name) @name
    parameters: (formal_parameters) @sig
    return_type: (_)? @sig) @def.method))

(namespace_use_clause (qualified_name) @import)
(namespace_use_clause (name) @import)
